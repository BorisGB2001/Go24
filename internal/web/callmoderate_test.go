package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCallModerate: the creator of a task leads its call, as «ведущий» — the page says so and the
// token carries it; the host turns off a participant's microphone, everyone's microphones but their own, and puts
// a participant out (a guest — for good: their link lets them in no more). An auditor of the task takes part but
// does not lead; someone outside the task does nothing.
func TestCallModerate(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, petr, outsider := newUser("Анна"), newUser("Пётр"), newUser("Посторонний")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "CM", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "CM")
	must(err)
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Планёрка", ResponsibleID: anna, Auditors: []string{petr}})
	must(err)
	room := "task-" + key

	// LiveKit: Пётр with his microphone and camera, a guest, Анна; what is muted and who is put out
	var mu sync.Mutex
	var muted, removed []string
	var guestID string
	lk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["room"] != room {
			w.WriteHeader(404)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/ListParticipants"):
			_ = json.NewEncoder(w).Encode(map[string]any{"participants": []map[string]any{
				{"identity": anna, "tracks": []map[string]any{{"sid": "TR_a", "source": "MICROPHONE"}}},
				{"identity": petr, "tracks": []map[string]any{{"sid": "TR_p1", "source": "MICROPHONE"}, {"sid": "TR_p2", "source": "CAMERA"}}},
				{"identity": calls.GuestIdentity(guestID), "tracks": []map[string]any{{"sid": "TR_g", "source": "MICROPHONE"}}}}})
		case strings.HasSuffix(r.URL.Path, "/MutePublishedTrack"):
			muted = append(muted, in["track_sid"].(string))
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/RemoveParticipant"):
			removed = append(removed, in["identity"].(string))
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer lk.Close()
	prov := calls.New("wss://lk", "key", "secret")
	prov.APIURL = lk.URL
	guests := &calls.Guests{DB: pool}
	srv := &Server{DB: pool, Tasks: ts, Calls: prov, CallGuests: guests, BaseURL: "http://go24",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Language", "ru")
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who}))
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	q := "?task=" + key

	if st, page := do(anna, "GET", "/call"+q, nil); st != http.StatusOK || !strings.Contains(page, `data-host="task=`) || !strings.Contains(page, "data-call-mute-all") {
		t.Fatalf("the host's page: %d", st)
	}
	if st, page := do(petr, "GET", "/call"+q, nil); st != http.StatusOK || strings.Contains(page, "data-host=") {
		t.Fatalf("an auditor's page: %d", st)
	}
	l, err := guests.CreateLink(ctx, anna, room, "Планёрка", "task="+key, "", false, time.Hour)
	must(err)
	g, _, err := guests.Arrive(ctx, l, "Гость")
	must(err)
	guestID = g.ID

	mute := url.Values{"action": {"mute"}, "who": {petr}, "what": {"microphone"}}
	for _, who := range []string{petr, outsider} {
		if st, _ := do(who, "POST", "/call/moderate"+q, mute); st == http.StatusOK || len(muted) != 0 {
			t.Fatalf("not the host muted: %d %v", st, muted)
		}
	}
	if st, body := do(anna, "POST", "/call/moderate"+q, mute); st != http.StatusOK || strings.Join(muted, ",") != "TR_p1" {
		t.Fatalf("mute Пётр: %d %s %v", st, body, muted)
	}
	muted = nil
	if st, _ := do(anna, "POST", "/call/moderate"+q, url.Values{"action": {"mute"}, "what": {"camera"}, "who": {petr}}); st != http.StatusOK || strings.Join(muted, ",") != "TR_p2" {
		t.Fatalf("his camera: %d %v", st, muted)
	}
	muted = nil
	if st, _ := do(anna, "POST", "/call/moderate"+q, url.Values{"action": {"mute"}, "what": {"microphone"}}); st != http.StatusOK || strings.Join(muted, ",") != "TR_p1,TR_g" {
		t.Fatalf("everyone but the host: %d %v", st, muted)
	}
	if st, _ := do(anna, "POST", "/call/moderate"+q, url.Values{"action": {"mute"}, "what": {"everything"}}); st == http.StatusOK {
		t.Fatal("an unknown source")
	}
	if st, _ := do(anna, "POST", "/call/moderate"+q, url.Values{"action": {"remove"}, "who": {anna}}); st == http.StatusOK || len(removed) != 0 {
		t.Fatal("the host put themself out")
	}
	if st, _ := do(petr, "POST", "/call/moderate"+q, url.Values{"action": {"remove"}, "who": {anna}}); st == http.StatusOK || len(removed) != 0 {
		t.Fatal("not the host put someone out")
	}
	if st, _ := do(anna, "POST", "/call/moderate"+q, url.Values{"action": {"remove"}, "who": {calls.GuestIdentity(g.ID)}}); st != http.StatusOK || strings.Join(removed, ",") != calls.GuestIdentity(g.ID) {
		t.Fatalf("put the guest out: %d %v", st, removed)
	}
	if again, err := guests.Get(ctx, l, g.ID, ""); err == nil && again.State != calls.GuestDenied {
		t.Fatalf("the guest may come back: %+v", again)
	}
	var state string
	must(pool.QueryRow(ctx, `SELECT state FROM call_guests WHERE id::text = $1`, g.ID).Scan(&state))
	if state != calls.GuestDenied {
		t.Fatalf("the guest's state: %s", state)
	}
}
