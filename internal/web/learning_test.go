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

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestLearningHTTP: «Обучение» through the pages — the administrator makes a course with lessons, an employee does
// not; a head assigns it to the department's employee, who finds it in «Мои курсы», reads a lesson and marks it
// passed (on to the next one); the course's learners; a guest has no «Обучение».
func TestLearningHTTP(t *testing.T) {
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
	admin, head, worker, guest := newUser("Админов"), newUser("Начальников"), newUser("Работягин"), newUser("Гостев")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, guest)
	must(err)
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Компания"))
	root, _, err := cs.Tree(ctx)
	must(err)
	sales, err := cs.CreateDepartment(ctx, root.ID, "Продажи", head)
	must(err)
	must(cs.SetDepartments(ctx, "", worker, []string{sales}))

	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), I18n: bundle, Company: cs, Notify: notify.NewStore(pool), Keys: keys,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{admin, head, worker, guest} {
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
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Location")
	}

	// the administrator makes a published course for the sales department and two lessons
	st, _, loc := do(admin, "POST", "/company/learning", url.Values{"title": {"Продажи для новичков"}, "published": {"1"},
		"color": {"green"}, "access": {"dept:" + sales}})
	if st != http.StatusSeeOther || !strings.HasSuffix(loc, "/edit") {
		t.Fatalf("create: %d %s", st, loc)
	}
	course := strings.TrimSuffix(strings.TrimPrefix(loc, "/company/learning/"), "/edit")
	_, _, loc = do(admin, "POST", "/company/learning/"+course+"/l", url.Values{"title": {"Первый звонок"}, "chapter": {"Основы"},
		"body": {"# Приветствие\n\nНазовите **имя**."}, "more": {"1"}})
	if loc != "/company/learning/"+course+"/l/new" {
		t.Fatalf("save and add more: %s", loc)
	}
	do(admin, "POST", "/company/learning/"+course+"/l", url.Values{"title": {"Возражения"}, "chapter": {"Основы"}})
	lessons, err := cs.Lessons(ctx, course, "")
	must(err)
	if len(lessons) != 2 {
		t.Fatalf("lessons: %+v", lessons)
	}
	if _, body, _ := do(admin, "GET", "/company/learning/"+course+"/edit", nil); !strings.Contains(body, "Возражения") ||
		!strings.Contains(body, `name="access"`) {
		t.Fatal("the course's form")
	}

	// an employee neither makes courses nor assigns them
	if st, _, _ = do(worker, "POST", "/company/learning", url.Values{"title": {"Своё"}}); st == http.StatusSeeOther {
		t.Fatal("an employee made a course")
	}
	if st, _, _ = do(worker, "POST", "/company/learning/"+course+"/assign", url.Values{"to": {worker}}); st == http.StatusSeeOther {
		t.Fatal("an employee assigned a course")
	}
	if _, body, _ := do(worker, "GET", "/company/learning", nil); strings.Contains(body, "tab=people") || strings.Contains(body, "/company/learning/new") {
		t.Fatal("an employee has «Обучение сотрудников» or «Создать курс»")
	}

	// the head assigns it to the department's employee with a due day: notified
	if st, _, _ = do(head, "POST", "/company/learning/"+course+"/assign", url.Values{"to": {"dept:" + sales}, "due": {"2026-12-01"}}); st != http.StatusSeeOther {
		t.Fatalf("the head assigns: %d", st)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'learn_assign'`, worker).Scan(&notes))
	if notes != 1 {
		t.Fatalf("notifications: %d", notes)
	}
	if _, body, _ := do(worker, "GET", "/company/learning", nil); !strings.Contains(body, "Продажи для новичков") || !strings.Contains(body, "01.12.2026") {
		t.Fatal("«Мои курсы» without the assigned course")
	}

	// the lesson: Markdown, «Урок пройден» goes on to the next one, the progress
	_, body, _ := do(worker, "GET", "/company/learning/"+course+"/l/"+lessons[0].ID, nil)
	if !strings.Contains(body, "<b>имя</b>") || !strings.Contains(body, "Приветствие") {
		t.Fatalf("the lesson: %s", body)
	}
	_, _, loc = do(worker, "POST", "/company/learning/"+course+"/l/"+lessons[0].ID+"/done", url.Values{})
	if loc != "/company/learning/"+course+"/l/"+lessons[1].ID {
		t.Fatalf("after the lesson: %s", loc)
	}
	_, _, loc = do(worker, "POST", "/company/learning/"+course+"/l/"+lessons[1].ID+"/done", url.Values{})
	if loc != "/company/learning/"+course {
		t.Fatalf("after the last lesson: %s", loc)
	}
	if _, body, _ = do(head, "GET", "/company/learning?tab=people&status=finished", nil); !strings.Contains(body, "Работягин") || !strings.Contains(body, "2/2") {
		t.Fatal("the head does not see the finished course")
	}
	if _, body, _ = do(admin, "GET", "/company/learning/"+course, nil); !strings.Contains(body, `id="learners"`) || !strings.Contains(body, "Работягин") {
		t.Fatal("the course's learners")
	}

	// a final test: made by the administrator, failed and then passed by the employee; the certificate
	_, _, loc = do(admin, "POST", "/company/learning/"+course+"/l", url.Values{"title": {"Итог"}, "kind": {"test"}, "pass_pct": {"100"}})
	if !strings.HasSuffix(loc, "/edit#questions") {
		t.Fatalf("a new test: %s", loc)
	}
	test := strings.TrimSuffix(strings.TrimPrefix(loc, "/company/learning/"+course+"/l/"), "/edit#questions")
	if st, _, _ = do(admin, "POST", "/company/learning/"+course+"/l/"+test+"/q", url.Values{"kind": {"one"}, "text": {"Сколько будет 2+2?"},
		"options": {"3\n*4\n5"}, "points": {"1"}}); st != http.StatusSeeOther {
		t.Fatalf("a question: %d", st)
	}
	if _, body, _ = do(admin, "GET", "/company/learning/"+course+"/l/"+test+"/edit", nil); !strings.Contains(body, "Сколько будет 2") || !strings.Contains(body, `id="qform"`) {
		t.Fatal("the test's form without its question")
	}
	qs, err := cs.Questions(ctx, test)
	must(err)
	if c, _ := cs.Course(ctx, course, worker, false); c.Finished() {
		t.Fatal("the course passed before the test")
	}
	attempt := func(answer string) string {
		t.Helper()
		_, _, loc := do(worker, "POST", "/company/learning/"+course+"/l/"+test+"/start", url.Values{})
		if !strings.Contains(loc, "/a/") {
			t.Fatalf("start: %s", loc)
		}
		if _, body, _ := do(worker, "GET", loc, nil); !strings.Contains(body, `name="q_`+qs[0].ID+`"`) {
			t.Fatal("the attempt's questions")
		}
		do(worker, "POST", loc, url.Values{"q_" + qs[0].ID: {answer}})
		_, body, _ := do(worker, "GET", loc, nil)
		return body
	}
	if body = attempt("0"); !strings.Contains(body, "Тест не сдан") {
		t.Fatal("a wrong answer passed")
	}
	if body = attempt("1"); !strings.Contains(body, "Тест сдан") || !strings.Contains(body, "1 из 1") {
		t.Fatal("the right answer failed")
	}
	if st, body, _ = do(worker, "GET", "/company/learning/"+course+"/certificate", nil); st != http.StatusOK || !strings.Contains(body, "Работягин") ||
		!strings.Contains(body, "Продажи для новичков") {
		t.Fatalf("the certificate: %d", st)
	}
	if st, _, _ = do(head, "GET", "/company/learning/"+course+"/certificate", nil); st == http.StatusOK {
		t.Fatal("a certificate of a course not passed")
	}
	if st, body, _ = do(head, "GET", "/company/learning/"+course+"/certificate?user="+worker, nil); st != http.StatusOK || !strings.Contains(body, "Работягин") {
		t.Fatalf("the head opens the employee's certificate: %d", st)
	}
	if _, body, _ = do(admin, "GET", "/u/"+worker, nil); !strings.Contains(body, `id="pcourses"`) {
		t.Fatal("the profile without the courses passed")
	}

	// «Обучение сотрудников» as CSV; REST with the rights of the pages
	if _, body, _ = do(head, "GET", "/company/learning?tab=people&format=csv", nil); !strings.Contains(body, "Работягин") || !strings.Contains(body, "100%") {
		t.Fatalf("csv: %s", body)
	}
	api := func(who, method, path, body string) (int, string) {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, body := api(worker, "GET", "/api/v1/learning/courses?mine=1", ""); st != http.StatusOK || !strings.Contains(body, `"finished": true`) {
		t.Fatalf("api courses: %d %s", st, body)
	}
	if st, body := api(worker, "GET", "/api/v1/learning/courses/"+course, ""); st != http.StatusOK || !strings.Contains(body, `"kind": "test"`) {
		t.Fatalf("api course: %d %s", st, body)
	}
	if st, _ := api(worker, "GET", "/api/v1/learning/assignments", ""); st != http.StatusForbidden {
		t.Fatalf("api assignments for an employee: %d", st)
	}
	if st, body := api(head, "GET", "/api/v1/learning/assignments?status=finished", ""); st != http.StatusOK || !strings.Contains(body, `"best_test_pct": 100`) {
		t.Fatalf("api assignments: %d %s", st, body)
	}
	if st, body := api(admin, "POST", "/api/v1/learning/assignments", `{"course_id":"`+course+`","to":["`+head+`"],"due":"2026-12-24"}`); st != http.StatusOK || !strings.Contains(body, `"assigned": 1`) {
		t.Fatalf("api assign: %d %q", st, body)
	}
	if st, _ := api(admin, "DELETE", "/api/v1/learning/assignments?course_id="+course+"&user_id="+head, ""); st != http.StatusNoContent {
		t.Fatalf("api unassign: %d", st)
	}

	// a guest has no «Обучение»
	if st, _, _ = do(guest, "GET", "/company/learning", nil); st != http.StatusNotFound {
		t.Fatalf("a guest: %d", st)
	}
}
