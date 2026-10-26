package notifyout

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/notify"
)

// fakeVK is the VK API and the Long Poll server of the community «go24club» (key «good»); events are numbered from
// 10; person 666 does not let the community write.
type fakeVK struct {
	mu     sync.Mutex
	url    string
	events []map[string]any
	sent   []string
}

func (f *fakeVK) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	enc := json.NewEncoder(w)
	if r.URL.Path == "/lp" {
		if r.URL.Query().Get("key") != "k1" {
			_ = enc.Encode(map[string]any{"failed": 2})
			return
		}
		from, _ := strconv.Atoi(r.URL.Query().Get("ts"))
		var out []map[string]any
		for i, e := range f.events {
			if 10+i >= from {
				out = append(out, e)
			}
		}
		_ = enc.Encode(map[string]any{"ts": strconv.Itoa(10 + len(f.events)), "updates": out})
		return
	}
	_ = r.ParseForm()
	if r.PostForm.Get("access_token") != "good" || r.URL.Query().Get("access_token") != "" {
		_ = enc.Encode(map[string]any{"error": map[string]any{"error_code": 5, "error_msg": "User authorization failed"}})
		return
	}
	switch r.URL.Path {
	case "/groups.getById":
		_ = enc.Encode(map[string]any{"response": map[string]any{"groups": []any{map[string]any{"id": 5, "screen_name": "go24club"}}}})
	case "/groups.getLongPollServer":
		_ = enc.Encode(map[string]any{"response": map[string]any{"server": f.url + "/lp", "key": "k1", "ts": strconv.Itoa(10 + len(f.events))}})
	case "/messages.send":
		if r.PostForm.Get("peer_id") == "666" {
			_ = enc.Encode(map[string]any{"error": map[string]any{"error_code": 901, "error_msg": "Can't send messages for users without permission"}})
			return
		}
		f.sent = append(f.sent, r.PostForm.Get("peer_id")+": "+r.PostForm.Get("message"))
		_ = enc.Encode(map[string]any{"response": len(f.sent)})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeVK) write(from, peer int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, map[string]any{"type": "message_new", "object": map[string]any{
		"message": map[string]any{"from_id": from, "peer_id": peer, "text": text}}})
}

func (f *fakeVK) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

// TestVK: the community is set by its access key (a wrong one refused, the token never in an address); the messages
// before are not answered; the code written in the community's messages links the person (not in a chat), the
// notifications go with the link, /stop unlinks, a person who forbids the messages is unlinked.
func TestVK(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'Анна') RETURNING id`).Scan(&anna))
	note := func(kind string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, body) VALUES ($1, $2, '') RETURNING id::text`, anna, kind).Scan(&id))
		return id
	}
	fv := &fakeVK{}
	srv := httptest.NewServer(fv)
	defer srv.Close()
	fv.url = srv.URL
	s := New(pool, "go24 secret")
	s.Channels = []Channel{&VK{API: srv.URL, HTTP: srv.Client()}}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "https://go24.test", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление " + it.Kind }
	s.Tr = func(key string, args ...any) string {
		if len(args) > 0 {
			return key + " " + args[0].(string)
		}
		return key
	}

	if err := s.SaveBot(ctx, "vk", true, "", "bad", false, anna); err == nil {
		t.Fatal("a wrong key was kept")
	}
	fv.write(100, 100, "старое") // before: not answered
	must(s.SaveBot(ctx, "vk", true, "", "good", false, anna))
	if b, err := s.Bot(ctx, "vk"); err != nil || !b.Ready() || b.Name != "go24club" {
		t.Fatalf("the community: %+v %v", b, err)
	}
	s.Pass(ctx)
	l, err := s.StartLink(ctx, anna, "vk")
	must(err)
	if l.URL != "" {
		t.Fatalf("a link with the code: %+v", l)
	}
	fv.write(100, 2000000001, l.Code) // a chat: not taken
	fv.write(100, 100, l.Code)
	s.Pass(ctx)
	if got := fv.took(); !slices.Equal(got, []string{"100: notifyout.bot_linked Анна"}) {
		t.Fatalf("the community's answers: %q", got)
	}
	if links, _ := s.Links(ctx, anna); len(links) != 1 || !links[0].Linked || links[0].Account != "vk.com/id100" {
		t.Fatalf("linked: %+v", links)
	}
	s.Pass(ctx)
	if got := fv.took(); len(got) != 0 {
		t.Fatalf("answered again: %q", got)
	}
	c := note("comment")
	s.Pass(ctx)
	if got := fv.took(); !slices.Equal(got, []string{"100: уведомление comment\nhttps://go24.test/notifications/" + c}) {
		t.Fatalf("delivered: %q", got)
	}
	fv.write(100, 100, "/stop")
	s.Pass(ctx)
	if got := fv.took(); !slices.Equal(got, []string{"100: notifyout.bot_unlinked"}) {
		t.Fatalf("/stop: %q", got)
	}
	l, err = s.StartLink(ctx, anna, "vk")
	must(err)
	fv.write(666, 666, l.Code)
	s.Pass(ctx)
	note("comment")
	s.Pass(ctx)
	if links, _ := s.Links(ctx, anna); links[0].Linked {
		t.Fatal("a person who forbids the messages kept the link")
	}
}
