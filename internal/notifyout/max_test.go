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

// fakeMax is the MAX Bot API of one bot: token «good», events queued by the test, chat 666 has blocked the bot.
type fakeMax struct {
	mu      sync.Mutex
	updates []map[string]any
	sent    []string
}

func (f *fakeMax) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "good" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":"verify.token","message":"Invalid access_token"}`)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /me":
		_, _ = io.WriteString(w, `{"user_id":1,"name":"go24","username":"go24_max_bot","is_bot":true}`)
	case "GET /updates":
		from, _ := strconv.Atoi(r.URL.Query().Get("marker"))
		var out []map[string]any
		if from < len(f.updates) {
			out = f.updates[from:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"updates": out, "marker": len(f.updates)})
	case "POST /messages":
		chat := r.URL.Query().Get("chat_id")
		if chat == "666" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"chat.denied","message":"bot is blocked"}`)
			return
		}
		var body struct{ Text, Format string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.sent = append(f.sent, chat+": "+body.Text+" "+body.Format)
		_, _ = io.WriteString(w, `{"message":{}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeMax) started(chat int64, user, payload string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"update_type": "bot_started", "chat_id": chat,
		"user": map[string]any{"username": user, "name": user}, "payload": payload})
}

func (f *fakeMax) write(chat int64, kind, user, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"update_type": "message_created", "message": map[string]any{
		"sender": map[string]any{"name": user}, "recipient": map[string]any{"chat_id": chat, "chat_type": kind},
		"body": map[string]any{"text": text}}})
}

func (f *fakeMax) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

// TestMax: the MAX bot is set by its token (a wrong one refused), the bot's link opens the dialog with the code and
// links it, the notifications go there with a link, a group's messages are not taken, a blocked bot unlinks.
func TestMax(t *testing.T) {
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
	fm := &fakeMax{}
	srv := httptest.NewServer(fm)
	defer srv.Close()
	s := New(pool, "go24 secret")
	s.Channels = []Channel{&Max{API: srv.URL, HTTP: srv.Client()}}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "https://go24.test", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление " + it.Kind }
	s.Tr = func(key string, args ...any) string {
		if len(args) > 0 {
			return key + " " + args[0].(string)
		}
		return key
	}

	if err := s.SaveBot(ctx, "max", true, "", "bad", false, anna); err == nil {
		t.Fatal("a wrong token was kept")
	}
	must(s.SaveBot(ctx, "max", true, "", "good", false, anna))
	if b, err := s.Bot(ctx, "max"); err != nil || !b.Ready() || b.Name != "go24_max_bot" {
		t.Fatalf("the bot: %+v %v", b, err)
	}
	l, err := s.StartLink(ctx, anna, "max")
	must(err)
	if l.URL != "https://max.ru/go24_max_bot?start="+l.Code {
		t.Fatalf("the link: %+v", l)
	}
	fm.started(200, "", "")               // the bot opened without a code: a greeting
	fm.write(300, "chat", "Анна", l.Code) // a group: not taken
	fm.started(200, "anna", l.Code)
	s.Pass(ctx)
	if got := fm.took(); !slices.Equal(got, []string{"200: notifyout.bot_hello ", "200: notifyout.bot_linked Анна "}) {
		t.Fatalf("the bot's answers: %q", got)
	}
	links, err := s.Links(ctx, anna)
	must(err)
	if len(links) != 1 || !links[0].Linked || links[0].Account != "@anna" {
		t.Fatalf("linked: %+v", links)
	}
	s.Pass(ctx) // the events are read once
	if got := fm.took(); len(got) != 0 {
		t.Fatalf("answered again: %q", got)
	}
	c := note("comment")
	s.Pass(ctx)
	if got := fm.took(); !slices.Equal(got, []string{`200: <a href="https://go24.test/notifications/` + c + `">уведомление comment</a> html`}) {
		t.Fatalf("delivered: %q", got)
	}

	// a message «/stop» in the dialog unlinks; a blocked bot drops the link
	fm.write(200, "dialog", "Анна", "/stop")
	s.Pass(ctx)
	if got := fm.took(); !slices.Equal(got, []string{"200: notifyout.bot_unlinked "}) {
		t.Fatalf("/stop: %q", got)
	}
	l, err = s.StartLink(ctx, anna, "max")
	must(err)
	fm.started(666, "anna", l.Code)
	s.Pass(ctx)
	note("comment")
	s.Pass(ctx)
	if links, _ = s.Links(ctx, anna); links[0].Linked {
		t.Fatal("a blocked bot kept the link")
	}
}
