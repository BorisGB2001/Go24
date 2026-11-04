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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestEnps: «eNPS» — an administrator and an HR officer with the right start a survey for a department (its
// sub-departments, head and deputies included), the others may not; the audience is notified and reminded in the
// top bar until they answer, once; nobody else sees the survey; the answer keeps no person; the results show from
// three answers, a department too; finishing, the index of the survey before; REST /api/v1/enps.
func TestEnps(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.FixedZone("UTC+2", 2*3600) // dates from Postgres are UTC midnights: «today» must stay today
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
	admin, hr, head, a, b, c, d := newUser("Админов"), newUser("Кадрова"), newUser("Главный"), newUser("Аня"), newUser("Боря"),
		newUser("Вика"), newUser("Гена")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Компания"))
	root, _, err := cs.Tree(ctx)
	must(err)
	sales, err := cs.CreateDepartment(ctx, root.ID, "Продажи", "")
	must(err)
	b2b, err := cs.CreateDepartment(ctx, sales, "Корпоративные", "")
	must(err)
	other, err := cs.CreateDepartment(ctx, root.ID, "Склад", "")
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $4), ($2, $4), ($3, $4), ($5, $6)`, a, b, c, b2b, d, other)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE departments SET head_id = $1 WHERE id = $2`, head, sales) // the head is not a member
	must(err)
	keys := apikeys.NewStore(pool)
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		return k
	}
	adminKey, dKey := key(admin), key(d)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, Company: cs, Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{admin, hr, head, a, b, c, d} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u, Admin: u == admin}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
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
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out), res.Header.Get("Location")
	}
	api := func(k, method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out)
	}
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format(time.DateOnly) }
	// answer: the answer is taken (back to the survey with the thanks, not with a message)
	answer := func(who, id string, score int, comment string) bool {
		t.Helper()
		_, _, loc := do(who, "POST", "/company/enps/"+id+"/answer", url.Values{"score": {strconv.Itoa(score)}, "comment": {comment}})
		return strings.HasSuffix(loc, "?thanks=1")
	}

	// who starts a survey: an administrator, an HR officer with the right; not an employee
	if _, body, _ := do(admin, "GET", "/company/enps", nil); !strings.Contains(body, `action="/company/enps"`) {
		t.Fatal("the administrator has no form")
	}
	if _, body, _ := do(a, "GET", "/company/enps", nil); strings.Contains(body, `action="/company/enps"`) {
		t.Error("an employee sees the form")
	}
	form := url.Values{"question": {"Порекомендуете?"}, "from": {day(0)}, "to": {day(7)}, "dept": {sales}}
	if st, _, _ := do(a, "POST", "/company/enps", form); st != http.StatusForbidden {
		t.Fatalf("an employee starts a survey: %d", st)
	}
	if st, _, _ := do(hr, "POST", "/company/enps", form); st != http.StatusForbidden {
		t.Fatalf("an HR officer without the right: %d", st)
	}
	bad := url.Values{"question": {"Q"}, "from": {day(3)}, "to": {day(0)}}
	do(admin, "POST", "/company/enps", bad)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM enps_surveys`).Scan(&n))
	if n != 0 {
		t.Fatal("the end before the start was taken")
	}
	must(cs.SetHR(ctx, hr, true, []string{"enps"}))
	st, _, loc := do(hr, "POST", "/company/enps", form)
	if st != http.StatusSeeOther || !strings.HasPrefix(loc, "/company/enps/") {
		t.Fatalf("the HR officer with the right: %d %s", st, loc)
	}
	id := strings.TrimPrefix(loc, "/company/enps/")

	// the audience: the department with its sub-department and its head; notified, all but the one who started it
	must(pool.QueryRow(ctx, `SELECT count(*) FROM enps_audience WHERE survey_id = $1`, id).Scan(&n))
	if n != 4 {
		t.Fatalf("audience %d, want the head and three employees", n)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'enps' AND body = 'Порекомендуете?' AND url = $1`, loc).Scan(&n))
	if n != 4 {
		t.Errorf("notifications: %d", n)
	}
	if st, _, _ := do(d, "GET", loc, nil); st != http.StatusNotFound {
		t.Errorf("one not asked opens the survey: %d", st)
	}
	if answer(d, id, 10, "") {
		t.Error("one not asked answered")
	}

	// the top bar reminds until the answer; the answer form; once
	if _, body, _ := do(a, "GET", "/company", nil); !strings.Contains(body, `href="/company/enps/`+id+`"`) {
		t.Error("no reminder in the top bar")
	}
	if _, body, _ := do(a, "GET", loc, nil); !strings.Contains(body, `name="score" value="10"`) {
		t.Error("no answer form")
	}
	if answer(a, id, 11, "") {
		t.Error("a score of 11 was taken")
	}
	if !answer(a, id, 9, "Хорошая команда") {
		t.Fatal("the answer is not taken")
	}
	if answer(a, id, 0, "") {
		t.Error("answered twice")
	}
	if _, body, _ := do(a, "GET", "/company", nil); strings.Contains(body, `href="/company/enps/`+id+`"`) {
		t.Error("the reminder stays after the answer")
	}
	if _, body, _ := do(a, "GET", loc, nil); strings.Contains(body, `name="score"`) || strings.Contains(body, "Хорошая команда") {
		t.Error("after the answer: the form again, or the results to an employee")
	}

	// one answer: the results hide all but the count
	if _, body, _ := do(admin, "GET", loc, nil); strings.Contains(body, "Хорошая команда") || !strings.Contains(body, bundle.T("ru-RU", "enps.too_few", 3)) {
		t.Error("one answer shows the results")
	}
	if !answer(b, id, 10, "") {
		t.Fatal("the answer is not taken")
	}
	if !answer(c, id, 3, "Мало платят") {
		t.Fatal("the answer is not taken")
	}
	if !answer(head, id, 7, "") {
		t.Fatal("the answer is not taken")
	}
	// the answers keep no person
	var cols []string
	rows, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name = 'enps_answers'`)
	must(err)
	for rows.Next() {
		var col string
		must(rows.Scan(&col))
		cols = append(cols, col)
	}
	if s := strings.Join(cols, " "); strings.Contains(s, "user") || strings.Contains(s, "_at") {
		t.Errorf("enps_answers keeps a person or a time: %s", s)
	}

	// four answers: 2 promoters, 1 passive, 1 detractor — 25; «Корпоративные» (3 answers) shown — 33, the head's «Продажи» (1) not
	st, out := api(adminKey, "GET", "/api/v1/enps/"+id, "")
	var e apiEnpsOut
	must(json.Unmarshal([]byte(out), &e))
	if st != http.StatusOK || e.Results == nil || !e.Results.Shown || e.Index == nil || *e.Index != 25 || e.Answers != 4 || e.Audience != 4 {
		t.Fatalf("results: %d %s", st, out)
	}
	r := e.Results
	if len(r.Departments) != 1 || r.Departments[0].Name != "Корпоративные" || r.Departments[0].Index != 33 || r.Hidden != 1 {
		t.Errorf("by department: %+v hidden %d", r.Departments, r.Hidden)
	}
	if r.Scores[9] != 1 || r.Scores[10] != 1 || r.Scores[3] != 1 || r.Scores[7] != 1 || len(r.Comments) != 2 {
		t.Errorf("scores %v, comments %v", r.Scores, r.Comments)
	}
	if _, body, _ := do(hr, "GET", loc, nil); !strings.Contains(body, "Мало платят") || !strings.Contains(body, "Корпоративные") {
		t.Error("the page of the results lacks the comment or the department")
	}

	// a survey of the whole company; finished early, it takes no answers; the survey before is compared
	st, out = api(dKey, "POST", "/api/v1/enps", `{}`)
	if st != http.StatusForbidden {
		t.Errorf("an employee starts a survey through REST: %d %s", st, out)
	}
	st, out = api(adminKey, "POST", "/api/v1/enps", `{"from":"`+day(0)+`","to":"`+day(3)+`"}`)
	var e2 apiEnpsOut
	must(json.Unmarshal([]byte(out), &e2))
	if st != http.StatusCreated || e2.Audience != 7 || len(e2.Departments) != 0 || e2.Question == "" || e2.Status != "open" {
		t.Fatalf("POST /enps: %d %s", st, out)
	}
	id2 := strconv.FormatInt(e2.ID, 10)
	if st, _ := api(dKey, "POST", "/api/v1/enps/"+id2+"/answer", `{"score":8}`); st != http.StatusNoContent {
		t.Errorf("an answer through REST: %d", st)
	}
	if st, _ := api(dKey, "POST", "/api/v1/enps/"+id2+"/answer", `{}`); st == http.StatusNoContent {
		t.Error("an answer without a score")
	}
	_, out = api(dKey, "GET", "/api/v1/enps", "")
	var mine []apiEnpsOut
	must(json.Unmarshal([]byte(out), &mine))
	if len(mine) != 1 || mine[0].ID != e2.ID || !mine[0].Answered || mine[0].Results != nil || mine[0].Index != nil {
		t.Errorf("an employee's list: %s", out)
	}
	if st, _ := api(dKey, "POST", "/api/v1/enps/"+id2+"/close", ""); st != http.StatusForbidden {
		t.Errorf("an employee finishes: %d", st)
	}
	if st, _, _ := do(admin, "POST", "/company/enps/"+id2+"/close", url.Values{}); st != http.StatusSeeOther {
		t.Fatalf("finishing: %d", st)
	}
	if answer(a, id2, 9, "") {
		t.Error("a finished survey took an answer")
	}
	if _, body, _ := do(a, "GET", "/company", nil); strings.Contains(body, `href="/company/enps/`+id2+`"`) {
		t.Error("a finished survey is reminded of")
	}
	_, out = api(adminKey, "GET", "/api/v1/enps/"+id2, "")
	must(json.Unmarshal([]byte(out), &e2))
	if e2.Status != "closed" || e2.Results.Shown || e2.Results.PrevID != e.ID || e2.Results.PrevIndex == nil || *e2.Results.PrevIndex != 25 {
		t.Errorf("the finished survey: %s", out)
	}
	_, out = api(adminKey, "GET", "/api/v1/enps", "")
	var all []apiEnpsOut
	must(json.Unmarshal([]byte(out), &all))
	if len(all) != 2 || all[1].Index == nil || *all[1].Index != 25 || all[0].Index != nil {
		t.Errorf("the keeper's list: %s", out)
	}

	// deleting
	if st, _, _ := do(a, "POST", "/company/enps/"+id+"/delete", url.Values{}); st != http.StatusForbidden {
		t.Errorf("an employee deletes: %d", st)
	}
	if st, _ := api(adminKey, "DELETE", "/api/v1/enps/"+id, ""); st != http.StatusNoContent {
		t.Errorf("DELETE: %d", st)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM enps_answers WHERE survey_id = $1`, id).Scan(&n))
	if n != 0 {
		t.Errorf("answers left: %d", n)
	}
	if st, _, _ := do(admin, "POST", "/company/enps/"+id2+"/delete", url.Values{}); st != http.StatusSeeOther {
		t.Errorf("deleting on the page: %d", st)
	}
}
