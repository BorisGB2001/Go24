package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestHRRights: «Сотрудники» for Excel and «Отсутствуют»; «Пригласить сотрудника» — administrators and an HR officer an administrator let invite, the others
// do not see it and get 403; an administrator gives the role; the HR officer with «absences» enters anyone's
// absence and everyone concerned is told; the report of vacations is the HR officer's and the managers'; the
// reminder of the vacation plan goes away with «в этом году не напоминать».
func TestHRRights(t *testing.T) {
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
	admin, hr, plain := newUser("Админов"), newUser("Кадрова"), newUser("Простов")
	cs := company.NewStore(pool)
	srv := &Server{
		DB: pool, Tasks: tasks.NewStore(pool), Company: cs, Notify: notify.NewStore(pool),
		BaseURL:  "http://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{admin, hr, plain} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u, Admin: u == admin}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(cookie[who])
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	invite := func(who string, want int, menu bool) {
		t.Helper()
		if st, _ := do(who, "GET", "/company/invite", nil); st != want {
			t.Errorf("%s opens the invitations: %d", who, st)
		}
		if st, _ := do(who, "POST", "/company/invite", url.Values{"email": {"new@t"}, "name": {"Новый"}}); want == http.StatusForbidden && st != want {
			t.Errorf("%s invites: %d", who, st)
		}
		if _, body := do(who, "GET", "/company", nil); strings.Contains(body, `href="/company/invite"`) != menu {
			t.Errorf("%s: «Пригласить сотрудника» in the menu = %v", who, !menu)
		}
	}
	invite(admin, http.StatusOK, true)
	invite(plain, http.StatusForbidden, false)
	invite(hr, http.StatusForbidden, false)

	// the role: an administrator gives it, nobody else
	if st, _ := do(plain, "POST", "/u/"+hr+"/hr", url.Values{"hr": {"1"}, "rights": {"invite"}}); st != http.StatusForbidden {
		t.Fatalf("an employee gives the role: %d", st)
	}
	if st, _ := do(admin, "POST", "/u/"+hr+"/hr", url.Values{"hr": {"1"}, "rights": {"invite"}}); st != http.StatusSeeOther {
		t.Fatalf("the administrator gives the role: %d", st)
	}
	invite(hr, http.StatusOK, true)
	if _, body := do(admin, "GET", "/u/"+hr, nil); !strings.Contains(body, `action="/u/`+hr+`/hr"`) {
		t.Error("no role form in the card for the administrator")
	}

	// absences: with «invite» only — not anyone's; with «absences» — anyone's, and all three are told
	oct := url.Values{"user": {plain}, "kind": {"vacation"}, "from": {"2026-11-02"}, "to": {"2026-11-06"}}
	if st, _ := do(hr, "POST", "/company/absences", oct); st == http.StatusSeeOther {
		t.Fatal("the HR officer without «absences» entered another's absence")
	}
	must(cs.SetHR(ctx, hr, true, []string{"absences", "invite"}))
	if st, _ := do(hr, "POST", "/company/absences", oct); st != http.StatusSeeOther {
		t.Fatalf("the HR officer enters an absence: %d", st)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'absence_hr' AND user_id IN ($1, $2)`, hr, plain).Scan(&notes))
	if notes != 2 {
		t.Fatalf("notifications = %d", notes)
	}
	if _, body := do(plain, "GET", "/notifications", nil); !strings.Contains(body, "Кадрова") || !strings.Contains(body, "02.11.2026 — 06.11.2026") {
		t.Error("the employee's notification does not tell of the absence")
	}

	// the report: the HR officer's; an employee with nobody under them — 403; CSV for Excel
	if st, _ := do(plain, "GET", "/company/absences/report", nil); st != http.StatusForbidden {
		t.Errorf("an employee opens the report: %d", st)
	}
	st, body := do(hr, "GET", "/company/absences/report?year=2026", nil)
	if st != http.StatusOK || !strings.Contains(body, "Простов") || !strings.Contains(body, "02.11.2026") {
		t.Errorf("the report: %d", st)
	}
	if st, body = do(hr, "GET", "/company/absences/report?year=2026&format=csv", nil); st != http.StatusOK ||
		!strings.HasPrefix(body, "\uFEFF") || !strings.Contains(body, "Простов;;02.11.2026 — 06.11.2026;5") {
		t.Errorf("the CSV: %d %q", st, body)
	}

	// «Сотрудники»: the list for Excel; «Отсутствуют» — those absent today; the tiles are remembered
	if st, body := do(plain, "GET", "/company?format=csv", nil); st != http.StatusOK || !strings.HasPrefix(body, "\uFEFF") || !strings.Contains(body, "Кадрова;") {
		t.Errorf("employees CSV: %d %q", st, body)
	}
	must(cs.AddAbsence(ctx, admin, admin, "sick", time.Now(), time.Now(), "", true, "", nil))
	if _, body := do(plain, "GET", "/company?preset=absent", nil); !strings.Contains(body, "Админов") || strings.Contains(body, "Простов</b>") {
		t.Error("«Отсутствуют» lists the wrong people")
	}
	if _, body := do(plain, "GET", "/company?view=tiles", nil); !strings.Contains(body, `class="card emptile"`) {
		t.Error("no tiles")
	}

	// the reminder: Админов has no vacation this year — until «в этом году не напоминать»
	if _, body := do(admin, "GET", "/company", nil); !strings.Contains(body, "vacation-remind-off") {
		t.Fatal("no reminder of the vacation plan")
	}
	if st, _ := do(admin, "POST", "/company/vacation-remind-off", url.Values{"back": {"/company"}}); st != http.StatusSeeOther {
		t.Fatalf("«не напоминать»: %d", st)
	}
	if _, body := do(admin, "GET", "/company", nil); strings.Contains(body, "vacation-remind-off") {
		t.Errorf("the reminder after «не напоминать» in %d", time.Now().Year())
	}

	// «Команды» (GO24DEV-689): an administrator adds one under the company, the head changes its people (not its
	// head), an employee — nothing; it shows in the structure, the list by team and the profile
	root, _, err := cs.Tree(ctx)
	must(err)
	if st, _ := do(plain, "POST", "/company/teams", url.Values{"op": {"add"}, "department": {root.ID}, "name": {"Альфа"}}); st != http.StatusForbidden {
		t.Errorf("an employee adds a team: %d", st)
	}
	if st, _ := do(admin, "POST", "/company/teams", url.Values{"op": {"add"}, "department": {root.ID}, "name": {"Альфа"}}); st != http.StatusSeeOther {
		t.Fatalf("a team added: %d", st)
	}
	teams, err := cs.Teams(ctx, "")
	must(err)
	if len(teams) != 1 {
		t.Fatalf("teams: %+v", teams)
	}
	team := teams[0].ID
	edit := func(who string, v url.Values) int {
		t.Helper()
		v.Set("op", "update")
		v.Set("id", team)
		st, _ := do(who, "POST", "/company/teams", v)
		return st
	}
	if st := edit(admin, url.Values{"department": {root.ID}, "name": {"Альфа"}, "head": {hr}, "members": {plain}}); st != http.StatusSeeOther {
		t.Fatalf("the administrator saves the team: %d", st)
	}
	if st := edit(plain, url.Values{"name": {"Моя"}}); st != http.StatusForbidden {
		t.Errorf("a member saves the team: %d", st)
	}
	if st := edit(hr, url.Values{"name": {"Альфа"}, "description": {"Новый продукт"}, "head": {plain}, "members": {plain, admin}}); st != http.StatusSeeOther {
		t.Fatalf("the head saves the team: %d", st)
	}
	tm, err := cs.Team(ctx, team)
	must(err)
	if tm.Head == nil || tm.Head.ID != hr || len(tm.Members) != 2 || tm.Description != "Новый продукт" {
		t.Fatalf("the team: %+v", tm)
	}
	_, body = do(hr, "GET", "/company/structure", nil)
	if !strings.Contains(body, `data-team="`+team+`"`) || !strings.Contains(body, "Новый продукт") || !strings.Contains(body, `name="members"`) ||
		strings.Contains(body, `<select name="department">`) {
		t.Error("the structure for the team's head: the team with its form, without the department")
	}
	if _, body = do(plain, "GET", "/company/structure", nil); !strings.Contains(body, "Альфа") || strings.Contains(body, `action="/company/teams"`) {
		t.Error("the structure for an employee: the team without forms")
	}
	if _, body = do(admin, "GET", "/company/structure", nil); !strings.Contains(body, `<select name="department">`) || !strings.Contains(body, `value="delete"`) {
		t.Error("the structure for an administrator: the department and «Удалить команду»")
	}
	if _, body = do(plain, "GET", "/company?team="+team, nil); !strings.Contains(body, "Админов") || !strings.Contains(body, "Кадрова") ||
		!strings.Contains(body, `name="team" value="`+team+`"`) {
		t.Error("the list by team")
	}
	if _, body = do(plain, "GET", "/u/"+plain, nil); !strings.Contains(body, `href="/company?team=`+team+`"`) {
		t.Error("no team in the profile")
	}
	if st, _ := do(admin, "POST", "/company/teams", url.Values{"op": {"delete"}, "id": {team}}); st != http.StatusSeeOther {
		t.Fatalf("the team deleted: %d", st)
	}
}
