package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/tasks"
)

// fakeChats is a messenger of group chats: who made each and who is in it.
type fakeChats struct {
	mu      sync.Mutex
	owner   map[string]string
	members map[string][]string
	names   map[string]string
	topics  map[string]string
}

func (f *fakeChats) CreateGroup(_ context.Context, userID, name string, ids []string, _ bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	room := "!r" + string(rune('0'+len(f.owner))) + ":x"
	f.owner[room], f.names[room], f.members[room] = userID, name, append([]string{userID}, ids...)
	return room, nil
}

func (f *fakeChats) AddToGroup(_ context.Context, userID, room string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		if !slices.Contains(f.members[room], id) {
			f.members[room] = append(f.members[room], id)
		}
	}
	return nil
}

func (f *fakeChats) SetGroupTopic(_ context.Context, userID, room, topic string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.topics[room] = topic
	return nil
}

// TestEventChatHTTP: «Чат встречи» — made by the organizer with the people who have not declined, opened again the
// same; one invited later comes into it, one who declined does not get it; a meeting without people has none.
func TestEventChatHTTP(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, eve, bob, kim := user("Анна"), user("Ева"), user("Боб"), user("Ким")
	chats := &fakeChats{owner: map[string]string{}, members: map[string][]string{}, names: map[string]string{}, topics: map[string]string{}}
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calendar: calendar.NewStore(pool), Chats: chats, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve, bob, kim} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.Local)
	ev, err := srv.Calendar.Save(ctx, anna, calendar.Event{Title: "Планёрка", Start: start, End: start.Add(time.Hour)}, []string{eve, bob})
	must(err)
	must(srv.Calendar.Respond(ctx, bob, ev, "declined"))

	_, page, _ := do(eve, "GET", "/calendar/"+ev, nil)
	if !strings.Contains(page, `action="/calendar/`+ev+`/chat"`) {
		t.Fatal("the meeting's page has no «Чат встречи»")
	}
	if _, page, _ := do(bob, "GET", "/calendar/"+ev, nil); strings.Contains(page, `/chat"`) {
		t.Fatal("one who declined has the chat")
	}
	if code, _, _ := do(kim, "POST", "/calendar/"+ev+"/chat", nil); code == http.StatusSeeOther {
		t.Fatal("a stranger opened the meeting's chat")
	}

	// Ева opens it: the organizer makes it with her (not Боб), the topic has the time and the link
	_, _, loc := do(eve, "POST", "/calendar/"+ev+"/chat", nil)
	var room string
	must(pool.QueryRow(ctx, `SELECT chat_room FROM events WHERE id::text = $1`, ev).Scan(&room))
	if room == "" || loc != imRoomURL(room) || chats.owner[room] != anna || chats.names[room] != "Планёрка" ||
		!slices.Equal(chats.members[room], []string{anna, eve}) {
		t.Fatalf("the chat %q → %s: %v %v", room, loc, chats.owner, chats.members)
	}
	if !strings.Contains(chats.topics[room], "12.11.2026 10:00") || !strings.Contains(chats.topics[room], "https://go24.test/calendar/"+ev) {
		t.Fatalf("the chat's topic %q", chats.topics[room])
	}
	if _, _, loc := do(anna, "POST", "/calendar/"+ev+"/chat", nil); loc != imRoomURL(room) || len(chats.owner) != 1 {
		t.Fatal("the chat made again")
	}

	// Ким invited later comes into it; Боб changes his mind and opens it — he is added
	do(anna, "POST", "/calendar/"+ev, url.Values{"title": {"Планёрка"}, "start": {start.Format("2006-01-02T15:04")},
		"end": {start.Add(time.Hour).Format("2006-01-02T15:04")}, "invite": {eve, bob, kim}})
	if !slices.Contains(chats.members[room], kim) || slices.Contains(chats.members[room], bob) {
		t.Fatalf("after the invitation %v", chats.members[room])
	}
	must(srv.Calendar.Respond(ctx, bob, ev, "accepted"))
	do(bob, "POST", "/calendar/"+ev+"/chat", nil)
	if !slices.Contains(chats.members[room], bob) {
		t.Fatal("Боб not added")
	}

	// a meeting of its organizer alone has no chat
	solo, err := srv.Calendar.Save(ctx, anna, calendar.Event{Title: "Одна", Start: start, End: start.Add(time.Hour)}, nil)
	must(err)
	if do(anna, "POST", "/calendar/"+solo+"/chat", nil); len(chats.owner) != 1 {
		t.Fatal("a chat of nobody")
	}
}
