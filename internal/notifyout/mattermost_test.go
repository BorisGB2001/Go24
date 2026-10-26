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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/notify"
)

// fakeMattermost is a Mattermost server with the bot «go24bot» (token «good»), its direct channels with Anna (dm1)
// and a gone person (dm6), and a group channel; posts are written by the test.
type fakeMattermost struct {
	mu    sync.Mutex
	clock int64
	posts map[string][]map[string]any // channel → posts
	sent  []string
}

func (f *fakeMattermost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer good" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Invalid or expired session"}`)
		return
	}
	enc := json.NewEncoder(w)
	path := strings.TrimPrefix(r.URL.Path, "/api/v4")
	switch {
	case path == "/users/me":
		_ = enc.Encode(map[string]any{"id": "bot", "username": "go24bot"})
	case path == "/users/me/teams":
		_ = enc.Encode([]map[string]any{{"id": "t1"}, {"id": "t2"}})
	case strings.HasPrefix(path, "/users/me/teams/"):
		var out []map[string]any
		for _, id := range []string{"dm1", "dm6", "grp"} {
			last := int64(0)
			for _, p := range f.posts[id] {
				last = max(last, p["create_at"].(int64))
			}
			kind := "D"
			if id == "grp" {
				kind = "G"
			}
			out = append(out, map[string]any{"id": id, "type": kind, "last_post_at": last})
		}
		_ = enc.Encode(out)
	case strings.HasPrefix(path, "/users/"):
		_ = enc.Encode(map[string]any{"id": "anna", "username": "anna"})
	case strings.HasPrefix(path, "/channels/"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/channels/"), "/posts")
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		posts := map[string]any{}
		for i, p := range f.posts[id] {
			if p["create_at"].(int64) > since {
				posts[id+strconv.Itoa(i)] = p
			}
		}
		_ = enc.Encode(map[string]any{"order": []string{}, "posts": posts})
	case path == "/posts" && r.Method == http.MethodPost:
		var p struct {
			Channel string `json:"channel_id"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p.Channel == "dm6" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"You do not have the appropriate permissions."}`)
			return
		}
		f.sent = append(f.sent, p.Channel+": "+p.Message)
		f.clock++
		f.posts[p.Channel] = append(f.posts[p.Channel], map[string]any{"user_id": "bot", "message": p.Message, "create_at": f.clock, "channel_id": p.Channel, "type": ""})
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeMattermost) write(channel, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock++
	f.posts[channel] = append(f.posts[channel], map[string]any{"user_id": "anna", "message": text, "create_at": f.clock, "channel_id": channel, "type": ""})
}

func (f *fakeMattermost) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

// TestMattermost: the bot is set by the server's address and its token (a wrong one or no address refused); the
// history before is not answered; the code written to the bot in its direct channel links it (not in a group), the
// notifications are posts with a link, /stop unlinks, a channel the bot cannot write drops the link.
func TestMattermost(t *testing.T) {
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
	fm := &fakeMattermost{clock: 1000, posts: map[string][]map[string]any{}}
	srv := httptest.NewServer(fm)
	defer srv.Close()
	s := New(pool, "go24 secret")
	s.Channels = []Channel{&Mattermost{HTTP: srv.Client(), now: func() time.Time { return time.UnixMilli(fm.clock) }}}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "https://go24.test", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление [" + it.Kind + "]" }
	s.Tr = func(key string, args ...any) string {
		if len(args) > 0 {
			return key + " " + args[0].(string)
		}
		return key
	}

	if err := s.SaveBot(ctx, "mattermost", true, "", "good", false, anna); err == nil {
		t.Fatal("a bot without the server's address")
	}
	if err := s.SaveBot(ctx, "mattermost", true, srv.URL, "bad", false, anna); err == nil {
		t.Fatal("a wrong token was kept")
	}
	fm.write("dm1", "старое сообщение") // before the bot: not answered
	must(s.SaveBot(ctx, "mattermost", true, srv.URL+"/", "good", false, anna))
	if b, err := s.Bot(ctx, "mattermost"); err != nil || !b.Ready() || b.Name != "go24bot" || b.Server != srv.URL {
		t.Fatalf("the bot: %+v %v", b, err)
	}
	s.Pass(ctx) // the first time: only the time is noted
	l, err := s.StartLink(ctx, anna, "mattermost")
	must(err)
	if l.URL != "" || l.Code == "" {
		t.Fatalf("the link: %+v", l)
	}
	fm.write("grp", l.Code) // a group: not taken
	fm.write("dm1", "/start "+l.Code)
	s.Pass(ctx)
	if got := fm.took(); !slices.Equal(got, []string{"dm1: notifyout.bot_linked Анна"}) {
		t.Fatalf("the bot's answers: %q", got)
	}
	if links, _ := s.Links(ctx, anna); len(links) != 1 || !links[0].Linked || links[0].Account != "@anna" {
		t.Fatalf("linked: %+v", links)
	}
	s.Pass(ctx) // read once, the bot's own posts are not taken
	if got := fm.took(); len(got) != 0 {
		t.Fatalf("answered again: %q", got)
	}
	c := note("comment")
	s.Pass(ctx)
	if got := fm.took(); !slices.Equal(got, []string{`dm1: [уведомление \[comment\]](https://go24.test/notifications/` + c + `)`}) {
		t.Fatalf("delivered: %q", got)
	}
	fm.write("dm1", "/stop")
	s.Pass(ctx)
	if got := fm.took(); !slices.Equal(got, []string{"dm1: notifyout.bot_unlinked"}) {
		t.Fatalf("/stop: %q", got)
	}
	l, err = s.StartLink(ctx, anna, "mattermost")
	must(err)
	fm.write("dm6", l.Code)
	s.Pass(ctx)
	note("comment")
	s.Pass(ctx)
	if links, _ := s.Links(ctx, anna); links[0].Linked {
		t.Fatal("a channel the bot cannot write kept the link")
	}
}
