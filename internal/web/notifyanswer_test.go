package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestNotifyAnswer: confirming notifications (GO24DEV-896) — a meeting's invitation answered «Приду» /
// «Возможно» / «Не приду», a request to join a group and an invitation to a project accepted or declined in the bell
// itself and by REST (which needs the section's scope too); an answered notification has no buttons and is read.
func TestNotifyAnswer(t *testing.T) {
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
	boss, anna, vera := newUser("Борис"), newUser("Анна"), newUser("Вера")
	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		Notify: notify.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	call := func(token, path string, body any) (int, string) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", hs.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	note := func(who, kind string) string {
		t.Helper()
		var id int64
		must(pool.QueryRow(ctx, `SELECT id FROM notifications WHERE user_id = $1 AND kind = $2 ORDER BY id DESC LIMIT 1`, who, kind).Scan(&id))
		return strconv.FormatInt(id, 10)
	}
	read := func(id string) bool {
		var r bool
		must(pool.QueryRow(ctx, `SELECT read_at IS NOT NULL FROM notifications WHERE id = $1`, id).Scan(&r))
		return r
	}
	role := func(who, key string) string {
		var r string
		_ = pool.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN projects p ON p.id = m.project_id
			WHERE p.key = $1 AND m.user_id = $2`, key, who).Scan(&r)
		return r
	}

	// a meeting: Анна is invited, the bell shows the three answers
	var ev string
	start := time.Now().Add(24 * time.Hour)
	must(pool.QueryRow(ctx, `INSERT INTO events (owner_id, title, starts_at, ends_at) VALUES ($1, 'Планёрка', $2, $3) RETURNING id`,
		boss, start, start.Add(time.Hour)).Scan(&ev))
	_, err = pool.Exec(ctx, `INSERT INTO event_attendees (event_id, user_id, status) VALUES ($1, $2, 'invited')`, ev, anna)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, event_id, body) VALUES ($1, $2, 'event', $3, 'Планёрка')`, anna, boss, ev)
	must(err)
	nEv := note(anna, "event")
	if st, body := do(anna, "GET", "/notifications", nil); st != http.StatusOK || !strings.Contains(body, `data-nans="accepted"`) ||
		!strings.Contains(body, `data-nans="tentative"`) || !strings.Contains(body, `data-nans="declined"`) {
		t.Fatalf("meeting buttons: %d %s", st, body)
	}
	if st, _ := do(anna, "POST", "/notifications/"+nEv+"/answer", url.Values{"answer": {"accept"}}); st != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong answer: %d", st)
	}
	if st, _ := do(vera, "POST", "/notifications/"+nEv+"/answer", url.Values{"answer": {"accepted"}}); st != http.StatusNotFound {
		t.Fatalf("someone else's notification: %d", st)
	}
	st, body := do(anna, "POST", "/notifications/"+nEv+"/answer", url.Values{"answer": {"tentative"}})
	var out struct {
		HTML   string
		Unread int
	}
	must(json.Unmarshal([]byte(body), &out))
	var status string
	must(pool.QueryRow(ctx, `SELECT status FROM event_attendees WHERE event_id = $1 AND user_id = $2`, ev, anna).Scan(&status))
	if st != http.StatusOK || status != "tentative" || strings.Contains(out.HTML, "data-nans") || !strings.Contains(out.HTML, "<b>") ||
		!read(nEv) || out.Unread != 0 {
		t.Fatalf("meeting answered: %d %s %q", st, status, body)
	}
	var replies int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'event_reply' AND body = 'tentative'`, boss).Scan(&replies))
	if replies != 1 {
		t.Fatalf("the organizer is not told: %d", replies)
	}
	if st, _ := do(anna, "POST", "/notifications/"+nEv+"/answer", url.Values{"answer": {"accepted"}}); st != http.StatusUnprocessableEntity {
		t.Fatalf("answered twice: %d", st)
	}

	// a closed group: Вера asks to join, Борис accepts in the bell
	if st, _ := do(boss, "POST", "/projects", url.Values{"key": {"CG"}, "name": {"Группа"}, "privacy": {"closed"}}); st != http.StatusSeeOther {
		t.Fatalf("create: %d", st)
	}
	if st, _ := do(vera, "POST", "/p/CG/join", url.Values{"message": {"Возьмите"}}); st != http.StatusSeeOther {
		t.Fatalf("request: %d", st)
	}
	nReq := note(boss, "group_join_request")
	if _, body := do(boss, "GET", "/notifications", nil); !strings.Contains(body, `data-nans="accept"`) || !strings.Contains(body, `data-nans="decline"`) {
		t.Fatalf("request buttons: %s", body)
	}
	if st, body := do(boss, "POST", "/notifications/"+nReq+"/answer", url.Values{"answer": {"accept"}}); st != http.StatusOK ||
		strings.Contains(body, "data-nans") || role(vera, "CG") != "member" || !read(nReq) {
		t.Fatalf("request accepted: %d %q %s", st, role(vera, "CG"), body)
	}

	// an invitation to the project by REST: the key needs projects:write besides im:write
	p, err := ts.ProjectByKey(ctx, boss, "CG")
	must(err)
	must(ts.InviteMember(ctx, p, boss, anna, "member"))
	nInv := note(anna, "project_invite")
	imOnly, err := keys.Create(ctx, anna, "k", []string{"im:write"}, time.Hour)
	must(err)
	full, err := keys.Create(ctx, anna, "k", []string{"im:read", "im:write", "projects:write"}, time.Hour)
	must(err)
	if st, body := call(imOnly, "/api/v1/notifications/"+nInv+"/answer", map[string]string{"answer": "accept"}); st == http.StatusOK || role(anna, "CG") != "" {
		t.Fatalf("answered without the scope: %d %s", st, body)
	}
	var list []apiNotification
	req, _ := http.NewRequest("GET", hs.URL+"/api/v1/notifications", nil)
	req.Header.Set("Authorization", "Bearer "+full)
	res, err := http.DefaultClient.Do(req)
	must(err)
	must(json.NewDecoder(res.Body).Decode(&list))
	res.Body.Close()
	if len(list) == 0 || list[0].Kind != "project_invite" || strings.Join(list[0].Answers, ",") != "accept,decline" {
		t.Fatalf("REST list: %+v", list)
	}
	st, body = call(full, "/api/v1/notifications/"+nInv+"/answer", map[string]string{"answer": "decline"})
	var got apiNotification
	must(json.Unmarshal([]byte(body), &got))
	if st != http.StatusOK || len(got.Answers) != 0 || role(anna, "CG") != "" || !read(nInv) {
		t.Fatalf("invitation declined: %d %s", st, body)
	}
	if st, _ := call(full, "/api/v1/notifications/"+nInv+"/answer", map[string]string{"answer": "accept"}); st == http.StatusOK || role(anna, "CG") != "" {
		t.Fatalf("a declined invitation accepted: %d", st)
	}
}
