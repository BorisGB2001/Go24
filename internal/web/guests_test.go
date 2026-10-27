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
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestGuestsSeeOnlyTheirCollabs: a guest of a collab reaches nothing of the company — not its structure, feed,
// employees, CRM, mail, processes, projects and their tasks, drives, calendar, other collabs and their people —
// neither by the pages nor by the REST API, nor through search, the people picker, cards and files; and an employee
// does not see the guests of collabs they are not in. Inside their collab the guest works as a member.
func TestGuestsSeeOnlyTheirCollabs(t *testing.T) {
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
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest, birthday) VALUES ($1, $1, $1 || '@t', $2, current_date)
			RETURNING id`, name, guest).Scan(&id))
		return id
	}
	// Ольга runs the collab with the guest Гость Клиентов; Секретов is an employee outside it; Чужой Гость is a guest
	// of another collab (of Другов)
	owner, guest := newUser("Ольга Владелец", false), newUser("Гость Клиентов", true)
	secret, other, otherGuest := newUser("Секретов Сотрудник", false), newUser("Другов Сотрудник", false), newUser("Чужой Гость", true)

	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{
		DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), CRM: crm.NewStore(pool),
		Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		BaseURL:  "http://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{owner, guest, secret, other, otherGuest} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u, Admin: true}))
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
		if who != "" {
			req.AddCookie(cookie[who])
		}
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Location")
	}

	// the company: structure, feed, CRM, a project with a task, the company calendar, an absence, the company drive
	must(srv.Company.EnsureRoot(ctx, "Компания Секрет"))
	tree, _, err := srv.Company.Tree(ctx)
	must(err)
	must(srv.Company.AddDepartment(ctx, tree.ID, "Отдел Тайн", secret))
	_, flat, err := srv.Company.Tree(ctx)
	must(err)
	for _, d := range flat {
		if d.Name == "Отдел Тайн" {
			must(srv.Company.SetDepartments(ctx, "", secret, []string{d.ID}))
		}
	}
	_, err = srv.Feed.CreatePost(ctx, secret, "", "Новости", "Секретная новость компании", false)
	must(err)
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, secret, crm.Deal{Title: "Секретная сделка", PipelineID: pipes[0].ID})
	must(err)
	_, err = ts.CreateProject(ctx, secret, "SEC", "Секретный проект", "")
	must(err)
	sec, err := ts.ProjectByKey(ctx, secret, "SEC")
	must(err)
	secTask, err := ts.CreateTask(ctx, secret, sec, tasks.NewTask{Title: "Секретная задача"})
	must(err)
	day := time.Now().Add(2 * time.Hour)
	_, err = pool.Exec(ctx, `INSERT INTO events (owner_id, company, title, starts_at, ends_at) VALUES ($1, true, 'Секретное собрание', $2, $3)`,
		secret, day, day.Add(time.Hour))
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', current_date, current_date, $1)`, secret)
	must(err)
	var companyFile, feedFile string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive) VALUES ('k1', 'Секретный файл.txt', 0, $1, 'company')
		RETURNING id`, secret).Scan(&companyFile))
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, post_id) SELECT 'k2', 'Вложение.txt', 0, $1, id FROM feed_posts LIMIT 1
		RETURNING id`, secret).Scan(&feedFile))

	// the collab of the guest, and another one with another guest
	clKey, err := ts.CreateCollab(ctx, owner, "Проект для Клиента", "", []string{guest})
	must(err)
	cl, err := ts.CollabByKey(ctx, owner, clKey)
	must(err)
	clTask, err := ts.CreateTask(ctx, owner, cl, tasks.NewTask{Title: "Макет для клиента", ResponsibleID: guest})
	must(err)
	cl2Key, err := ts.CreateCollab(ctx, other, "Чужой коллаб", "", []string{otherGuest})
	must(err)
	cl2, err := ts.CollabByKey(ctx, other, cl2Key)
	must(err)
	cl2Task, err := ts.CreateTask(ctx, other, cl2, tasks.NewTask{Title: "Чужая задача", ResponsibleID: otherGuest})
	must(err)

	// pages and actions of the company: not found for the guest — and there for an employee
	for _, p := range []string{"/feed", "/company", "/company/structure", "/company/absences", "/company/birthdays", "/company/invite",
		"/crm/deals", "/crm/deals/" + deal, "/crm/contacts", "/crm/roles", "/mail", "/mail/settings", "/mail/rcpt?q=a", "/mail/drive", "/mail/unread", "/mail/settings?tab=junk", "/bp", "/bp/templates",
		"/kb", "/openlines", "/projects", "/p/SEC", "/p/SEC/board", "/p/" + cl2Key, "/tasks", "/plan", "/deadlines", "/flows",
		"/templates", "/tasks/efficiency", "/tasks/trash", "/files", "/docs", "/disk/my", "/disk/company", "/disk/p/SEC", "/disk/p/" + cl2Key,
		"/settings/keys", "/settings/storages", "/settings/worktime", "/settings/webhooks", "/settings/task-fields", "/people/departments",
		"/worktime", "/worktime/table", "/worktime/reports", "/worktime/reports/new", "/worktime/d/" + secret + "/2026-10-01", "/t/" + secTask, "/t/" + cl2Task, "/u/" + secret, "/u/" + secret + "/card", "/u/" + otherGuest,
		"/calendar?project=SEC", "/calendar?project=" + cl2Key, "/calendar?user=" + secret} {
		if st, body, _ := do(guest, "GET", p, nil); st != http.StatusNotFound || strings.Contains(body, "Секрет") {
			t.Errorf("the guest opens %s: %d", p, st)
		}
	}
	for _, p := range []string{"/feed", "/company", "/company/structure", "/projects", "/p/SEC", "/t/" + secTask, "/u/" + secret, "/calendar?view=week&user=" + owner} {
		if st, _, _ := do(secret, "GET", p, nil); st != http.StatusOK {
			t.Errorf("an employee opens %s: %d", p, st)
		}
	}
	for _, f := range []url.Values{
		{"__path": {"/feed"}, "body": {"x"}}, {"__path": {"/projects"}, "key": {"GX"}, "name": {"x"}}, {"__path": {"/collabs"}, "name": {"x"}},
		{"__path": {"/p/SEC/tasks"}, "title": {"x"}}, {"__path": {"/crm/deals/new"}, "title": {"x"}}, {"__path": {"/admin/mode"}},
		{"__path": {"/company/absences"}}, {"__path": {"/settings/keys"}, "name": {"k"}}, {"__path": {"/p/" + clKey + "/members"}, "user_id": {guest}},
		{"__path": {"/p/" + clKey + "/guests"}, "kind": {"link"}}, {"__path": {"/u/" + guest + "/active"}, "active": {"0"}},
		{"__path": {"/calendar/people"}, "user": {secret}},
	} {
		p := f.Get("__path")
		f.Del("__path")
		if st, _, _ := do(guest, "POST", p, f); st != http.StatusNotFound {
			t.Errorf("the guest posts %s: %d", p, st)
		}
	}
	if ok, _ := ts.IsGuest(ctx, guest); !ok {
		t.Fatal("the guest stopped being a guest")
	}

	// the guest's own sections: their collab, its task, drive, calendar, their tasks, the people of the collab
	for _, p := range []string{"/collabs", "/p/" + clKey, "/p/" + clKey + "/board", "/disk/p/" + clKey, "/calendar?project=" + clKey,
		"/t/" + clTask, "/my", "/calendar", "/u/" + owner, "/u/" + guest, "/notifications", "/settings/notifications",
		"/tasks/new?project=" + clKey, "/p/" + clKey + "/gantt", "/search?q=" + url.QueryEscape("Макет"), "/calendar/new?project=" + clKey} {
		st, body, _ := do(guest, "GET", p, nil)
		if st != http.StatusOK {
			t.Errorf("the guest's %s: %d", p, st)
			continue
		}
		for _, leak := range []string{"Секрет", "Отдел Тайн", "Чуж", "Другов", `href="/feed"`, `href="/crm/deals"`, `href="/company"`, `href="/projects"`,
			`href="/mail"`, `href="/settings/keys"`, `href="/disk/my"`} {
			if strings.Contains(body, leak) {
				t.Errorf("the guest's %s shows %q", p, leak)
			}
		}
	}
	if _, body, _ := do(guest, "GET", "/collabs", nil); !strings.Contains(body, "Проект для Клиента") || !strings.Contains(body, `class="pill guest"`) {
		t.Error("the guest's collabs: no collab or no guest mark")
	}
	if _, body, _ := do(owner, "GET", "/t/"+clTask, nil); !strings.Contains(body, `class="pill guest"`) {
		t.Error("the task card does not mark the guest")
	}
	if st, _, loc := do(guest, "GET", "/", nil); st != http.StatusFound || loc != "/collabs" {
		t.Errorf("the guest's start page: %d %s", st, loc)
	}

	// search, the people picker, cards and files
	for _, q := range []string{"Секретная", secTask, "Чужая", cl2Task} {
		if st, body, loc := do(guest, "GET", "/search?q="+url.QueryEscape(q), nil); st != http.StatusOK || loc != "" || strings.Contains(body, "Секретная задача") ||
			strings.Contains(body, "Чужая задача") {
			t.Errorf("the guest searches %q: %d %s", q, st, loc)
		}
	}
	people := func(who, q string) map[string]bool {
		t.Helper()
		st, body, _ := do(who, "GET", "/people?self=1&"+q, nil)
		if st != http.StatusOK {
			t.Fatalf("people %s: %d", q, st)
		}
		var list []map[string]any
		must(json.Unmarshal([]byte(body), &list))
		out := map[string]bool{}
		for _, p := range list {
			out[p["id"].(string)] = true
			if p["dept"] != nil && who == guest && p["dept"] != "" {
				t.Errorf("the guest sees a department: %v", p)
			}
		}
		return out
	}
	for _, q := range []string{"", "q=С", "recent=1", "dept=" + tree.ID + "&deep=1", "ids=" + secret + "," + otherGuest + "," + owner} {
		got := people(guest, q)
		if got[secret] || got[other] || got[otherGuest] {
			t.Errorf("the guest's picker (%s) shows people outside the collab: %v", q, got)
		}
	}
	if got := people(guest, ""); !got[owner] || !got[guest] {
		t.Errorf("the guest's picker misses the collab's people: %v", got)
	}
	if got := people(secret, ""); got[guest] || got[otherGuest] || !got[owner] || !got[other] {
		t.Errorf("an employee's picker shows guests of other collabs: %v", got)
	}
	if got := people(owner, ""); !got[guest] || got[otherGuest] {
		t.Errorf("the owner's picker: %v", got)
	}
	if st, body, _ := do(owner, "GET", "/people?q=Гость", nil); st != http.StatusOK || !strings.Contains(body, `"guest": true`) {
		t.Errorf("the picker does not mark a guest: %s", body)
	}
	for _, p := range []string{"/u/" + guest, "/u/" + guest + "/card", "/u/" + otherGuest} {
		if st, _, _ := do(secret, "GET", p, nil); st != http.StatusNotFound {
			t.Errorf("an employee outside the collab opens the guest's %s: %d", p, st)
		}
	}
	if st, body, _ := do(owner, "GET", "/u/"+guest, nil); st != http.StatusOK || !strings.Contains(body, `class="pill guest"`) {
		t.Errorf("the guest's card for the owner: %d", st)
	}
	if st, _, _ := do(secret, "GET", "/company?q=Гость", nil); st != http.StatusOK {
		t.Error("employees page")
	} else if _, body, _ := do(secret, "GET", "/company", nil); strings.Contains(body, "Гость Клиентов") || strings.Contains(body, "Чужой Гость") {
		t.Error("guests are listed among the employees")
	}
	// «Гости коллабов» of an administrator: the guests with their collabs, no employees; not for others
	if _, body, _ := do(secret, "GET", "/company?guests=1", nil); !strings.Contains(body, "Гость Клиентов") || !strings.Contains(body, "Чужой Гость") ||
		!strings.Contains(body, `href="/p/`+clKey+`">Проект для Клиента</a>`) || strings.Contains(body, "Ольга Владелец") {
		t.Error("the administrator's list of guests")
	}
	if _, body, _ := do(secret, "GET", "/company?guests=1&format=csv", nil); !strings.Contains(body, "Чужой коллаб") || strings.Contains(body, "Секретов") {
		t.Error("the guests' CSV")
	}
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: secret, Name: secret}))
	cookie["plain"] = rec.Result().Cookies()[0]
	if _, body, _ := do("plain", "GET", "/company?guests=1", nil); strings.Contains(body, "Гость Клиентов") || strings.Contains(body, `href="/company?guests=1"`) {
		t.Error("guests listed for an employee who is not an administrator")
	}
	if _, body, _ := do(guest, "GET", "/u/"+owner, nil); strings.Contains(body, "/company?dept=") || strings.Contains(body, "Компания Секрет") {
		t.Error("the guest sees the structure on a card")
	}
	for _, f := range []string{companyFile, feedFile} {
		if st, _, _ := do(guest, "GET", "/files/"+f, nil); st != http.StatusForbidden {
			t.Errorf("the guest downloads a company file: %d", st)
		}
		if st, _, _ := do(secret, "GET", "/files/"+f, nil); st != http.StatusOK {
			t.Errorf("an employee downloads a company file: %d", st)
		}
	}
	if _, err := srv.Drive.Open(tasks.WithViewer(ctx, guest, true), guest, "company"); err == nil {
		t.Error("the guest opens the company drive")
	}

	// the calendar: no company meeting, absence or birthday of the company for the guest
	month := "/calendar?month=" + time.Now().Format("2006-01")
	if _, body, _ := do(guest, "GET", month, nil); strings.Contains(body, "Секретное собрание") || strings.Contains(body, "Секретов") {
		t.Error("the guest's calendar shows the company")
	}
	if _, body, _ := do(secret, "GET", month, nil); !strings.Contains(body, "Секретное собрание") || strings.Contains(body, "Гость Клиентов") {
		t.Error("an employee's calendar: no company meeting, or a guest's birthday")
	}
	// the schedule: the company's meeting for the employee, by the panel of calendars
	if _, body, _ := do(secret, "GET", "/calendar?view=list&date="+time.Now().AddDate(0, 0, -1).Format("2006-01-02"), nil); !strings.Contains(body, "Секретное собрание") ||
		!strings.Contains(body, `data-cal="company"`) || !strings.Contains(body, "data-calcals") {
		t.Error("the schedule lacks the company meeting")
	}
	if _, body, _ := do(guest, "GET", "/calendar?view=list", nil); strings.Contains(body, "Секретное собрание") {
		t.Error("the guest's schedule shows the company")
	}
	// «Создать задачу / встречу» of a feed post: its text for those who see it, nothing for the guest
	fpost, err := srv.Feed.CreatePost(ctx, secret, "", "Квартальный план", "Секретные цифры квартала", false)
	must(err)
	if _, body, _ := do(owner, "GET", "/tasks/new?feed="+fpost, nil); !strings.Contains(body, "Квартальный план") || !strings.Contains(body, "Секретные цифры квартала") {
		t.Error("a task of a feed post lacks the post")
	}
	if _, body, _ := do(owner, "GET", "/calendar/new?title="+url.QueryEscape("Созвон по плану"), nil); !strings.Contains(body, "Созвон по плану") {
		t.Error("a meeting of the feed's form lacks the title")
	}
	// reminders of the meeting form (GO24DEV-424): a new meeting offers 15 minutes before; several are saved
	if _, body, _ := do(owner, "GET", "/calendar/new", nil); !strings.Contains(body, `<option value="15" selected>`) || !strings.Contains(body, "data-remind-add") {
		t.Error("a new meeting lacks its reminder")
	}
	date := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	if st, _, _ := do(owner, "POST", "/calendar/new", url.Values{"title": {"Напоминания"}, "start": {date + "T10:00"}, "end": {date + "T11:00"},
		"remind": {"60", "15", "x"}}); st != http.StatusSeeOther {
		t.Fatalf("a meeting with reminders = %d", st)
	}
	var reminds string
	must(pool.QueryRow(ctx, `SELECT reminds::text FROM events WHERE title = 'Напоминания'`).Scan(&reminds))
	if reminds != "{15,60}" {
		t.Errorf("reminders saved = %s", reminds)
	}
	if _, body, _ := do(guest, "GET", "/calendar/new?feed="+fpost, nil); strings.Contains(body, "Секретные цифры") {
		t.Error("the guest got the company's post into a meeting")
	}
	// the planner of a meeting: the guest learns the busy time of the people of their collabs only
	busy := "/calendar/busy?start=" + time.Now().Format("2006-01-02") + "T10:00&end=" + time.Now().Format("2006-01-02") + "T11:00&user=" + secret + "&user=" + owner
	if st, body, _ := do(guest, "GET", busy, nil); st != http.StatusOK || strings.Contains(body, secret) || !strings.Contains(body, owner) {
		t.Errorf("the guest's planner: %d %s", st, body)
	}
	// dragging a meeting of the collab: its organizer moves it, the invited guest does not
	st0 := time.Now().Truncate(time.Hour).Add(48 * time.Hour)
	evID, err := srv.Calendar.Save(ctx, owner, calendar.Event{Title: "Созвон", Start: st0, End: st0.Add(time.Hour), ProjectID: cl.ID}, []string{guest})
	must(err)
	moveTo := url.Values{"start": {st0.Add(time.Hour).Format("2006-01-02T15:04")}, "end": {st0.Add(3 * time.Hour).Format("2006-01-02T15:04")}}
	if st, _, _ := do(guest, "POST", "/calendar/"+evID+"/move", moveTo); st != http.StatusForbidden {
		t.Errorf("the invited guest moved the meeting: %d", st)
	}
	if st, body, _ := do(owner, "POST", "/calendar/"+evID+"/move", moveTo); st != http.StatusOK {
		t.Errorf("the organizer's move: %d %s", st, body)
	}
	if e, err := srv.Calendar.Get(ctx, guest, evID); err != nil || !e.Start.Equal(st0.Add(time.Hour)) || !e.End.Equal(st0.Add(3*time.Hour)) || e.MyStatus != "invited" {
		t.Errorf("after the move: %+v %v", e, err)
	}
	list, err := srv.Calendar.Range(ctx, guest, time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 2))
	must(err)
	for _, e := range list {
		if e.Kind != "task" && e.ProjectID != cl.ID {
			t.Errorf("the guest's calendar: %+v", e)
		}
	}
	// a guest's meeting is in their collab, with its people; nobody invites a guest outside the collabs
	start := time.Now().Add(48 * time.Hour)
	if _, err := srv.Calendar.Save(ctx, guest, calendar.Event{Title: "Личная", Start: start, End: start.Add(time.Hour)}, nil); err == nil {
		t.Error("a guest made a meeting outside the collabs")
	}
	if _, err := srv.Calendar.Save(ctx, guest, calendar.Event{Title: "Созвон", Start: start, End: start.Add(time.Hour), ProjectID: cl.ID}, []string{secret}); err == nil {
		t.Error("a guest invited an employee outside the collab")
	}
	if _, err := srv.Calendar.Save(ctx, guest, calendar.Event{Title: "Созвон", Start: start, End: start.Add(time.Hour), ProjectID: cl.ID}, []string{owner}); err != nil {
		t.Errorf("a guest's meeting in the collab: %v", err)
	}
	if _, err := srv.Calendar.Save(ctx, secret, calendar.Event{Title: "Планёрка", Start: start, End: start.Add(time.Hour)}, []string{guest}); err == nil {
		t.Error("an employee invited a guest to a meeting of the company")
	}
	// tasks of the company never take a guest
	if _, err := ts.CreateTask(ctx, secret, sec, tasks.NewTask{Title: "С гостем", ResponsibleID: guest}); err == nil {
		t.Error("a guest became responsible for a task of the company")
	}
	if _, err := ts.CreateTask(ctx, secret, sec, tasks.NewTask{Title: "С гостем", Auditors: []string{guest}}); err == nil {
		t.Error("a guest became an auditor of a task of the company")
	}
	if err := ts.SetMember(ctx, sec, guest, "member"); err == nil {
		t.Error("a guest joined a project of the company")
	}
	if ok, _ := ts.KnowsPerson(ctx, guest, secret); ok {
		t.Error("the guest may write to an employee outside the collab")
	}
	if ok, _ := ts.KnowsPerson(ctx, guest, owner); !ok {
		t.Error("the guest may not write to the collab's owner")
	}
	if ok, _ := ts.KnowsPerson(ctx, otherGuest, guest); ok {
		t.Error("guests of different collabs know each other")
	}

	// the REST API of a guest's key
	token := func(u string) string {
		k, err := keys.Create(ctx, u, "k", []string{"collabs:read", "collabs:write", "projects:read", "projects:write", "tasks:read", "tasks:write",
			"crm:read", "crm:write", "mail:read", "im:read", "bp:write"}, time.Hour)
		must(err)
		return k
	}
	api := func(tok, method, path string, body any) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	gt := token(guest)
	for _, c := range [][2]string{{"GET", "/api/v1/crm/deals"}, {"GET", "/api/v1/crm/deals/" + deal}, {"GET", "/api/v1/crm/pipelines"},
		{"GET", "/api/v1/mail/boxes"}, {"GET", "/api/v1/mail/messages"}, {"GET", "/api/v1/flows"}, {"GET", "/api/v1/trash"},
		{"GET", "/api/v1/efficiency"}, {"GET", "/api/v1/projects/SEC"}, {"GET", "/api/v1/projects/SEC/tasks"}, {"GET", "/api/v1/tasks/" + secTask},
		{"GET", "/api/v1/tasks/" + cl2Task}, {"GET", "/api/v1/collabs/" + cl2Key}, {"POST", "/api/v1/projects"}, {"POST", "/api/v1/collabs"},
		{"POST", "/api/v1/bp/x/start"}} {
		if st, raw := api(gt, c[0], c[1], map[string]any{}); st != http.StatusNotFound || strings.Contains(raw, "Секрет") {
			t.Errorf("the guest's key: %s %s: %d %s", c[0], c[1], st, raw)
		}
	}
	for _, p := range []string{"/api/v1/projects", "/api/v1/tasks", "/api/v1/collabs", "/api/v1/collabs/" + clKey, "/api/v1/tasks/" + clTask, "/api/v1/me"} {
		st, raw := api(gt, "GET", p, nil)
		if st != http.StatusOK || strings.Contains(raw, "Секрет") || strings.Contains(raw, "Чуж") {
			t.Errorf("the guest's key: GET %s: %d %s", p, st, raw)
		}
	}
	if st, _ := api(token(secret), "GET", "/api/v1/crm/deals", nil); st != http.StatusOK {
		t.Errorf("an employee's key reads CRM: %d", st)
	}
}

// TestGuestInvites: the owner invites guests into a collab by a link (with a term, revocable), by e-mail and by
// phone (personal, once); a person opens the link and joins — a new one as a guest at their first login.
func TestGuestInvites(t *testing.T) {
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
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@t', $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	owner, emp, guest := newUser("Ольга", false), newUser("Пётр", false), newUser("Гость", true)
	ts := tasks.NewStore(pool)
	srv := &Server{
		DB: pool, Tasks: ts, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), Keys: apikeys.NewStore(pool),
		BaseURL: "http://go24.test", GuestGroups: []string{"go24-guests"}, AdminGroups: []string{"go24-admins"},
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{owner, emp, guest} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (*http.Response, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if who != "" {
			req.AddCookie(cookie[who])
		}
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	key, err := ts.CreateCollab(ctx, owner, "Сайт для Альфы", "", []string{guest})
	must(err)
	c, err := ts.CollabByKey(ctx, owner, key)
	must(err)

	// the owner makes a link for 7 days; a guest, an outsider or a bad form make none
	if res, _ := do(owner, "POST", "/p/"+key+"/guests", url.Values{"kind": {"link"}, "days": {"7"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("a link: %d", res.StatusCode)
	}
	if res, _ := do(emp, "POST", "/p/"+key+"/guests", url.Values{"kind": {"link"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("an outsider made a link: %d", res.StatusCode)
	}
	if res, _ := do(guest, "POST", "/p/"+key+"/guests", url.Values{"kind": {"link"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("a guest made a link: %d", res.StatusCode)
	}
	if _, err := ts.CreateGuestInvite(tasks.WithViewer(ctx, guest, true), c, guest, "link", "", "", 0); err == nil {
		t.Error("a guest invites")
	}
	if _, err := ts.CreateGuestInvite(ctx, c, owner, "email", "не почта", "", 0); err == nil {
		t.Error("an invitation to a bad e-mail")
	}
	if _, err := ts.CreateGuestInvite(ctx, c, owner, "phone", "12", "", 0); err == nil {
		t.Error("an invitation to a bad phone")
	}
	_, body := do(owner, "GET", "/p/"+key, nil)
	list, err := ts.GuestInvites(ctx, c, owner)
	must(err)
	if len(list) != 1 || list[0].Kind != "link" || list[0].Expires.Before(time.Now().Add(6*24*time.Hour)) ||
		!strings.Contains(body, "http://go24.test/join/"+list[0].Token) {
		t.Fatalf("the collab's invitations: %+v", list)
	}
	link := list[0]
	if _, body := do(emp, "GET", "/p/"+key, nil); strings.Contains(body, link.Token) {
		t.Error("an outsider sees the link")
	}
	if _, body := do(guest, "GET", "/p/"+key, nil); strings.Contains(body, link.Token) {
		t.Error("a guest sees the link")
	}

	// the link's page for anyone; an unknown link is not found
	res, body := do("", "GET", "/join/"+link.Token, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Сайт для Альфы") || !strings.Contains(body, "/join/"+link.Token+"/signup?login=1") {
		t.Fatalf("the link's page: %d", res.StatusCode)
	}
	if res, _ := do("", "GET", "/join/nope", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown link: %d", res.StatusCode)
	}
	// «войти»: the link waits in a cookie for the login
	res, _ = do("", "GET", "/join/"+link.Token+"/signup?login=1", nil)
	var kept string
	for _, ck := range res.Cookies() {
		if ck.Name == joinCookie {
			kept = ck.Value
		}
	}
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login" || kept != link.Token {
		t.Fatalf("sign in by the link: %d %s %q", res.StatusCode, res.Header.Get("Location"), kept)
	}

	// a new person signs in by the link: a guest, in the collab; one in the guests' group of Authentik is a guest too
	nu, active, isGuest, err := srv.signIn(ctx, auth.Claims{Sub: "new-1", Email: "new@client.example", Name: "Новый Клиент"}, link.Token)
	must(err)
	if !active || !isGuest {
		t.Fatalf("a newcomer by a link: active %v guest %v", active, isGuest)
	}
	got, err := ts.AcceptGuestInvite(ctx, link.Token, nu)
	must(err)
	if got != key {
		t.Fatalf("joined %s", got)
	}
	if p, err := ts.CollabByKey(ctx, nu, key); err != nil || p.MyRole != "member" {
		t.Fatalf("the newcomer in the collab: %v %+v", err, p)
	}
	if _, _, g, _ := srv.signIn(ctx, auth.Claims{Sub: "grp-1", Email: "g@x", Name: "Из группы", Groups: []string{"go24-guests", "go24-admins"}}, ""); !g {
		t.Error("one of the guests' group is not a guest")
	}
	if _, _, g, _ := srv.signIn(ctx, auth.Claims{Sub: "emp-9", Email: "e9@x", Name: "Новый сотрудник"}, ""); g {
		t.Error("a new employee became a guest")
	}
	if _, _, g, _ := srv.signIn(ctx, auth.Claims{Sub: "new-1", Email: "new@client.example", Name: "Новый Клиент"}, ""); !g {
		t.Error("a guest's next login made them an employee")
	}
	var empSub string
	must(pool.QueryRow(ctx, `SELECT oidc_sub FROM users WHERE id = $1`, emp).Scan(&empSub))
	if _, _, g, _ := srv.signIn(ctx, auth.Claims{Sub: empSub, Email: "Пётр@t", Name: "Пётр"}, link.Token); g {
		t.Error("an employee became a guest by a link")
	}

	// an employee signed in joins as a member, by «Присоединиться»
	if _, body := do(emp, "GET", "/join/"+link.Token, nil); !strings.Contains(body, `action="/join/`+link.Token+`"`) {
		t.Error("no «Присоединиться» for one signed in")
	}
	if res, _ := do(emp, "POST", "/join/"+link.Token, url.Values{}); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/c/"+key {
		t.Fatalf("join: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if g, _ := ts.IsGuest(ctx, emp); g {
		t.Error("the employee became a guest")
	}
	if res, _ := do(emp, "GET", "/join/"+link.Token, nil); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/c/"+key {
		t.Errorf("a member opens the link again: %d", res.StatusCode)
	}

	// revoked: the link no longer works, those who joined stay
	must(ts.RevokeGuestInvite(ctx, c, owner, link.ID))
	if _, err := ts.AcceptGuestInvite(ctx, link.Token, guest); err == nil {
		t.Error("a revoked link works")
	}
	if res, body := do("", "GET", "/join/"+link.Token, nil); res.StatusCode != http.StatusOK || strings.Contains(body, "/signup") {
		t.Errorf("the revoked link's page: %d", res.StatusCode)
	}
	if _, err := ts.CollabByKey(ctx, nu, key); err != nil {
		t.Error("the revoked link took the guest out")
	}

	// personal: by e-mail, once; by phone; expired
	byMail, err := ts.CreateGuestInvite(ctx, c, owner, "email", "Боб <Bob@Client.Example>", "", 0)
	must(err)
	if byMail.Email != "bob@client.example" || byMail.Name != "Боб" {
		t.Fatalf("by e-mail: %+v", byMail)
	}
	b1, _, _, err := srv.signIn(ctx, auth.Claims{Sub: "bob", Email: "bob@client.example", Name: "Боб"}, byMail.Token)
	must(err)
	_, err = ts.AcceptGuestInvite(ctx, byMail.Token, b1)
	must(err)
	b2, _, _, err := srv.signIn(ctx, auth.Claims{Sub: "bob2", Email: "x@client.example", Name: "Не Боб"}, byMail.Token)
	must(err)
	if _, err := ts.AcceptGuestInvite(ctx, byMail.Token, b2); err == nil {
		t.Error("a personal invitation worked twice")
	}
	if g, _ := ts.IsGuest(ctx, b2); g {
		t.Error("one come by a spent invitation became a guest")
	}
	byPhone, err := ts.CreateGuestInvite(ctx, c, owner, "phone", "+7 (999) 123-45-67", "Вера", 0)
	must(err)
	if byPhone.Phone != "+79991234567" {
		t.Fatalf("by phone: %+v", byPhone)
	}
	_, err = pool.Exec(ctx, `UPDATE collab_invites SET expires_at = now() - interval '1 minute' WHERE id = $1`, byPhone.ID)
	must(err)
	if _, err := ts.AcceptGuestInvite(ctx, byPhone.Token, emp); err == nil {
		t.Error("an expired invitation works")
	}
	// the owner sees them all with their state
	_, body = do(owner, "GET", "/p/"+key, nil)
	for _, want := range []string{"bob@client.example", "79991234567"} {
		if !strings.Contains(body, want) {
			t.Errorf("the collab's page does not list %s", want)
		}
	}
	// an archived collab takes nobody
	fresh, err := ts.CreateGuestInvite(ctx, c, owner, "link", "", "", 0)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE projects SET archived = true WHERE id = $1`, c.ID)
	must(err)
	if _, err := ts.AcceptGuestInvite(ctx, fresh.Token, emp); err == nil {
		t.Error("an archived collab took someone")
	}
}
