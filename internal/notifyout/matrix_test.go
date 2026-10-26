package notifyout

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// fakeMatrix is a Matrix server with the bot @go24:hs.test (token «good»): rooms with their members and messages,
// invitations of the bot; /sync gives what came after the position it handed out.
type fakeMatrix struct {
	mu      sync.Mutex
	clock   int64
	events  []map[string]any // each with "room"
	invites []string
	members map[string][]string
	sent    []string
}

func (f *fakeMatrix) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer good" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errcode":"M_UNKNOWN_TOKEN","error":"Invalid access token"}`)
		return
	}
	enc := json.NewEncoder(w)
	path, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/_matrix/client/v3"))
	switch {
	case path == "/account/whoami":
		_ = enc.Encode(map[string]string{"user_id": "@go24:hs.test"})
	case path == "/sync":
		from, _ := strconv.Atoi(r.URL.Query().Get("since"))
		join := map[string]any{}
		for _, e := range f.events[min(from, len(f.events)):] {
			room := e["room"].(string)
			j, _ := join[room].(map[string]any)
			if j == nil {
				j = map[string]any{"timeline": map[string]any{"events": []any{}}}
				join[room] = j
			}
			tl := j["timeline"].(map[string]any)
			tl["events"] = append(tl["events"].([]any), e)
		}
		invite := map[string]any{}
		for _, room := range f.invites {
			invite[room] = map[string]any{}
		}
		_ = enc.Encode(map[string]any{"next_batch": strconv.Itoa(len(f.events)), "rooms": map[string]any{"join": join, "invite": invite}})
	case strings.HasPrefix(path, "/rooms/") && strings.HasSuffix(path, "/join"):
		room := strings.TrimSuffix(strings.TrimPrefix(path, "/rooms/"), "/join")
		f.invites = slices.DeleteFunc(f.invites, func(x string) bool { return x == room })
		f.members[room] = append(f.members[room], "@go24:hs.test")
		_, _ = io.WriteString(w, `{"room_id":"`+room+`"}`)
	case strings.HasSuffix(path, "/joined_members"):
		room := strings.TrimSuffix(strings.TrimPrefix(path, "/rooms/"), "/joined_members")
		joined := map[string]any{}
		for _, u := range f.members[room] {
			joined[u] = map[string]any{}
		}
		_ = enc.Encode(map[string]any{"joined": joined})
	case strings.Contains(path, "/send/m.room.message/") && r.Method == http.MethodPut:
		room := strings.TrimPrefix(path[:strings.Index(path, "/send/")], "/rooms/")
		if !slices.Contains(f.members[room], "@go24:hs.test") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"errcode":"M_FORBIDDEN","error":"not in room"}`)
			return
		}
		var c map[string]string
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.sent = append(f.sent, room+": "+c["body"]+" | "+c["formatted_body"])
		f.clock++
		f.events = append(f.events, map[string]any{"room": room, "type": "m.room.message", "sender": "@go24:hs.test", "origin_server_ts": f.clock,
			"content": map[string]any{"msgtype": "m.text", "body": c["body"]}})
		_, _ = io.WriteString(w, `{"event_id":"$e"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeMatrix) write(room, sender, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock++
	f.events = append(f.events, map[string]any{"room": room, "type": "m.room.message", "sender": sender, "origin_server_ts": f.clock,
		"content": map[string]any{"msgtype": "m.text", "body": text}})
}

func (f *fakeMatrix) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

// TestMatrix: the bot is a Matrix account by the server's address and its token; it joins a chat it is invited to,
// links it by the code (a chat of three is not taken, the history before is not answered), the notifications are
// messages with a link, /stop unlinks, a chat the bot is out of drops the link.
func TestMatrix(t *testing.T) {
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
	fm := &fakeMatrix{clock: 5000, members: map[string][]string{"!dm:hs.test": {"@anna:other.org"}, "!grp:hs.test": {"@anna:other.org", "@bob:hs.test", "@go24:hs.test"}}}
	srv := httptest.NewServer(fm)
	defer srv.Close()
	s := New(pool, "go24 secret")
	s.Channels = []Channel{&Matrix{HTTP: srv.Client(), now: func() time.Time { return time.UnixMilli(fm.clock) }}}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "https://go24.test", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление " + it.Kind }
	s.Tr = func(key string, args ...any) string {
		if len(args) > 0 {
			return key + " " + args[0].(string)
		}
		return key
	}

	if err := s.SaveBot(ctx, "matrix", true, srv.URL, "bad", false, anna); err == nil {
		t.Fatal("a wrong token was kept")
	}
	fm.write("!grp:hs.test", "@bob:hs.test", "старое") // before the bot: not answered
	must(s.SaveBot(ctx, "matrix", true, srv.URL, "good", false, anna))
	if b, err := s.Bot(ctx, "matrix"); err != nil || !b.Ready() || b.Name != "@go24:hs.test" {
		t.Fatalf("the bot: %+v %v", b, err)
	}
	s.Pass(ctx)
	l, err := s.StartLink(ctx, anna, "matrix")
	must(err)
	if l.URL != "" {
		t.Fatalf("a link with the code: %+v", l)
	}
	fm.mu.Lock()
	fm.invites = append(fm.invites, "!dm:hs.test") // Anna starts a direct chat with the bot
	fm.mu.Unlock()
	s.Pass(ctx)
	fm.write("!grp:hs.test", "@anna:other.org", l.Code) // a chat of three: not taken
	fm.write("!dm:hs.test", "@anna:other.org", l.Code)
	s.Pass(ctx)
	if got := fm.took(); !slices.Equal(got, []string{"!dm:hs.test: notifyout.bot_linked Анна | "}) {
		t.Fatalf("the bot's answers: %q", got)
	}
	if links, _ := s.Links(ctx, anna); len(links) != 1 || !links[0].Linked || links[0].Account != "@anna:other.org" {
		t.Fatalf("linked: %+v", links)
	}
	s.Pass(ctx)
	if got := fm.took(); len(got) != 0 {
		t.Fatalf("answered again: %q", got)
	}
	c := note("comment")
	s.Pass(ctx)
	link := "https://go24.test/notifications/" + c
	if got := fm.took(); !slices.Equal(got, []string{"!dm:hs.test: уведомление comment " + link + ` | <a href="` + link + `">уведомление comment</a>`}) {
		t.Fatalf("delivered: %q", got)
	}
	fm.write("!dm:hs.test", "@anna:other.org", "/stop")
	s.Pass(ctx)
	if got := fm.took(); !slices.Equal(got, []string{"!dm:hs.test: notifyout.bot_unlinked | "}) {
		t.Fatalf("/stop: %q", got)
	}

	// the bot is out of the chat: the link goes
	l, err = s.StartLink(ctx, anna, "matrix")
	must(err)
	fm.write("!dm:hs.test", "@anna:other.org", l.Code)
	s.Pass(ctx)
	fm.took()
	fm.mu.Lock()
	fm.members["!dm:hs.test"] = []string{"@anna:other.org"}
	fm.mu.Unlock()
	note("comment")
	s.Pass(ctx)
	if links, _ := s.Links(ctx, anna); links[0].Linked {
		t.Fatal("a chat the bot is out of kept the link")
	}
}
