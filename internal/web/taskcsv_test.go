package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestTaskCSV: «Импорт задач» makes tasks of a CSV with people by name or e-mail, deadlines, tags, a checklist and the
// row's project, and tells the bad rows; «Экспорт в Excel» gives the list as filtered, and the file comes back in.
func TestTaskCSV(t *testing.T) {
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
	newUser := func(name, email string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $2) RETURNING id`, name, email).Scan(&id))
		return id
	}
	boss, anna, petr, outsider := newUser("Борис Г", "boris@t"), newUser("Анна Ли", "anna@t"), newUser("Пётр Семёнов", "petr@t"), newUser("Посторонний", "o@t")
	ts := tasks.NewStore(pool)
	for _, k := range []string{"AA", "BB"} {
		_, err := ts.CreateProject(ctx, boss, k, "Проект "+k, "")
		must(err)
	}
	srv := &Server{DB: pool, Tasks: ts, Company: company.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{boss, outsider} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who string, req *http.Request) (int, string, string) {
		t.Helper()
		req.AddCookie(cookie[who])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Location")
	}
	upload := func(who, project, data string) string {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		_ = mw.WriteField("project", project)
		fw, _ := mw.CreateFormFile("file", "tasks.csv")
		_, _ = fw.Write([]byte(data))
		_ = mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/tasks/import", &b)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		st, _, loc := send(who, req)
		if st != http.StatusSeeOther {
			t.Fatalf("import: %d", st)
		}
		msg, _ := url.ParseQuery(strings.SplitN(loc, "?", 2)[1])
		return msg.Get("msg")
	}

	msg := upload(boss, "AA", "\ufeffНазвание;Ответственный;Соисполнители;Наблюдатели;Крайний срок;Важная;Теги;Чек-лист;Проект;Описание\n"+
		"Отчёт;анна ли;petr@t;;31.12.2026 18:00;да;#отчёт, квартал;Собрать;;Годовой\n"+
		"Счёт;Петр Семенов;;Анна Ли;15.11.2026;;;;BB;\n"+
		";;;;;;;;;\n"+
		"Без людей;Иван Неизвестный;;;;;;;;\n"+
		"Плохой срок;;;;завтра;;;;;\n"+
		"Чужой проект;;;;;;;;ZZ;\n")
	if !strings.Contains(msg, "created: 2,") && !strings.Contains(msg, "Создано задач: 2,") {
		t.Fatalf("import message: %q", msg)
	}
	for _, want := range []string{"5", "6", "7"} { // the rows with errors, by their number in the file
		if !strings.Contains(msg, want) {
			t.Errorf("row %s is not told: %q", want, msg)
		}
	}
	list, err := ts.ListTasks(ctx, tasks.Filter{Viewer: boss})
	must(err)
	if len(list) != 2 {
		t.Fatalf("tasks made: %d", len(list))
	}
	byTitle := map[string]tasks.Task{}
	for _, x := range list {
		byTitle[x.Title] = x
	}
	rep, bill := byTitle["Отчёт"], byTitle["Счёт"]
	if rep.ProjectKey != "AA" || rep.ResponsibleID != anna || rep.CreatorID != boss || rep.Priority != 2 || rep.Description != "Годовой" ||
		rep.Deadline == nil || rep.Deadline.Format("2006-01-02 15:04") != "2026-12-31 18:00" || strings.Join(rep.Tags, ",") != "квартал,отчёт" && strings.Join(rep.Tags, ",") != "отчёт,квартал" {
		t.Errorf("«Отчёт» = %+v", rep)
	}
	if bill.ProjectKey != "BB" || bill.ResponsibleID != petr || bill.Deadline == nil || bill.Deadline.Format("15:04") != "18:00" {
		t.Errorf("«Счёт» = %+v (the row's project, a date alone ends with the working day)", bill)
	}
	names, err := ts.ParticipantNames(ctx, []string{rep.ID, bill.ID})
	must(err)
	if strings.Join(names[rep.ID]["accomplice"], "") != "Пётр Семёнов" || strings.Join(names[bill.ID]["auditor"], "") != "Анна Ли" {
		t.Errorf("participants = %v", names)
	}

	// export with the filter: only the important ones
	req, _ := http.NewRequest("GET", hs.URL+"/tasks/export?prio=high", nil)
	st, body, _ := send(boss, req)
	if st != http.StatusOK || !strings.HasPrefix(body, "\ufeff") {
		t.Fatalf("export: %d %q", st, body)
	}
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\ufeff")))
	cr.Comma = ';'
	rows, err := cr.ReadAll()
	must(err)
	if len(rows) != 2 || rows[1][0] != rep.Key() || rows[1][1] != "Отчёт" || rows[1][6] != "Пётр Семёнов" || rows[1][8] != "31.12.2026 18:00" {
		t.Fatalf("export rows = %q", rows)
	}
	// the exported file comes back in as it is
	if msg := upload(boss, "", body); !strings.Contains(msg, "задач: 1,") && !strings.Contains(msg, "created: 1,") {
		t.Errorf("re-import: %q", msg)
	}
	// someone else sees nothing of these projects: an empty export, an import into them refused
	req, _ = http.NewRequest("GET", hs.URL+"/tasks/export", nil)
	if _, body, _ := send(outsider, req); strings.Count(body, "\n") != 1 {
		t.Errorf("an outsider's export: %q", body)
	}
	if msg := upload(outsider, "", "Название;Проект\nВзлом;AA\n"); !strings.Contains(msg, "задач: 0,") && !strings.Contains(msg, "created: 0,") {
		t.Errorf("an outsider's import: %q", msg)
	}
	if n, _ := ts.ListTasks(ctx, tasks.Filter{Viewer: boss, Search: "Взлом"}); len(n) != 0 {
		t.Error("an outsider put a task into someone else's project")
	}
	// «Права доступа»: a role without export and import closes both
	roles, err := ts.AccessRoles(ctx)
	must(err)
	_, err = ts.SaveAccessRole(ctx, tasks.AccessRole{ID: roles[0].ID, Perms: tasks.AccessPerms{"view": "sub", "edit": "sub", "delete": "sub", "assign": "all"}})
	must(err)
	req, _ = http.NewRequest("GET", hs.URL+"/tasks/export", nil)
	if st, _, _ := send(boss, req); st != http.StatusForbidden {
		t.Errorf("an export without the right: %d", st)
	}
	req, _ = http.NewRequest("GET", hs.URL+"/tasks", nil)
	if _, page, _ := send(boss, req); strings.Contains(page, "/tasks/export") || strings.Contains(page, `action="/tasks/import"`) {
		t.Error("the list offers export or import without the right")
	}
}
