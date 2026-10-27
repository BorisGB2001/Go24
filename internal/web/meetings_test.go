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
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestMeetings: «Собрания и планёрки» — the organizer makes a meeting (it goes to the calendar with the people
// invited, they are notified), prepares the agenda (items, a sub-item, the order), starts it, writes decisions and
// sets a task on an item, finishes it and prints the minutes; a member sees but does not change it, an outsider
// does not see it; «Следующее собрание» carries the items not discussed and shows the open tasks; REST /api/v1/meetings.
func TestMeetings(t *testing.T) {
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
	boss, clerk, member, outsider := newUser("Начальников"), newUser("Секретарёва"), newUser("Участников"), newUser("Посторонний")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "OPS", "Операции", "")
	must(err)
	keys := apikeys.NewStore(pool)
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		return k
	}
	bossKey, memberKey := key(boss), key(member)
	bundle, err := i18n.Load("")
	must(err)
	cal := calendar.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, I18n: bundle, Company: company.NewStore(pool), Calendar: cal,
		Notify: notify.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{boss, clerk, member, outsider} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u}))
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
	api := func(k, method, path, body string, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if out != nil && res.StatusCode < 300 {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s %s: %v in %s", method, path, err, b)
			}
		}
		return res.StatusCode
	}
	start := time.Now().AddDate(0, 0, 1).Truncate(time.Hour)

	// the organizer makes the meeting: the calendar has it with the people invited, they are notified
	st, _, loc := do(boss, "POST", "/worktime/meetings", url.Values{"title": {"Планёрка продаж"}, "start": {start.Format("2006-01-02T15:04")},
		"minutes": {"30"}, "place": {"Переговорная 1"}, "owner": {boss}, "keeper": {clerk}, "members": {member}})
	if st != http.StatusSeeOther || !strings.HasPrefix(loc, "/worktime/meetings/") {
		t.Fatalf("create: %d %q", st, loc)
	}
	mid, _ := strconv.ParseInt(strings.TrimPrefix(loc, "/worktime/meetings/"), 10, 64)
	base := loc
	m, err := srv.Company.Meeting(ctx, boss, false, mid)
	must(err)
	if m.EventID == "" || m.Keeper.ID != clerk || len(m.Members) != 1 || m.Minutes != 30 {
		t.Fatalf("meeting: %+v", m)
	}
	ev, err := cal.Get(ctx, boss, m.EventID)
	must(err)
	if ev.Title != "Планёрка продаж" || !ev.Start.Equal(start) || !ev.End.Equal(start.Add(30*time.Minute)) || ev.Where != "Переговорная 1" || len(ev.Attendees) < 2 {
		t.Fatalf("calendar event: %+v", ev)
	}
	var notified int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'meeting' AND user_id IN ($1, $2)`, clerk, member).Scan(&notified))
	if notified != 2 {
		t.Fatalf("notified: %d", notified)
	}

	// who sees and who changes
	if st, body, _ := do(member, "GET", base, nil); st != http.StatusOK || !strings.Contains(body, "Планёрка продаж") || strings.Contains(body, base+"/items\"") {
		t.Fatalf("a member: %d, may add items = %v", st, strings.Contains(body, base+"/items\""))
	}
	if st, _, _ := do(member, "POST", base+"/items", url.Values{"title": {"Чужой вопрос"}}); st != http.StatusForbidden {
		t.Fatalf("a member adds an item: %d", st)
	}
	if st, _, _ := do(outsider, "GET", base, nil); st != http.StatusNotFound {
		t.Fatalf("an outsider: %d", st)
	}
	if _, body, _ := do(outsider, "GET", "/worktime/meetings", nil); strings.Contains(body, "Планёрка продаж") {
		t.Fatal("an outsider sees the meeting in the list")
	}
	if _, body, _ := do(member, "GET", "/worktime/meetings?role=owner", nil); strings.Contains(body, "Планёрка продаж") {
		t.Fatal("«Я организатор» of a member lists the meeting")
	}

	// the agenda: the secretary prepares it — two items, a sub-item, the order
	for _, title := range []string{"План на неделю", "Сделки в работе"} {
		if st, _, _ := do(clerk, "POST", base+"/items", url.Values{"title": {title}, "reporter": {member}, "minutes": {"10"}}); st != http.StatusSeeOther {
			t.Fatalf("add item %s: %d", title, st)
		}
	}
	m, err = srv.Company.Meeting(ctx, boss, false, mid)
	must(err)
	plan, deals := m.Items[0].ID, m.Items[1].ID
	do(clerk, "POST", base+"/items", url.Values{"title": {"Крупные клиенты"}, "parent": {strconv.FormatInt(deals, 10)}})
	do(clerk, "POST", base+"/items/"+strconv.FormatInt(deals, 10)+"/move", url.Values{"dir": {"up"}})
	m, err = srv.Company.Meeting(ctx, boss, false, mid)
	must(err)
	if len(m.Items) != 3 || m.Items[0].ID != deals || m.Items[1].Num != "1.1" || m.Items[2].ID != plan || m.Items[2].Num != "2" {
		t.Fatalf("agenda: %+v", m.Items)
	}

	// started: a decision and a task on the item; finished — the minutes
	do(boss, "POST", base+"/status", url.Values{"status": {"action"}})
	do(clerk, "POST", base+"/items/"+strconv.FormatInt(deals, 10), url.Values{"result": {"Дожать сделку с «Ромашкой»"}})
	st, _, _ = do(boss, "POST", base+"/items/"+strconv.FormatInt(deals, 10)+"/task", url.Values{"project": {"OPS"}, "title": {"Позвонить в «Ромашку»"},
		"responsible": {member}, "deadline": {start.AddDate(0, 0, 2).Format("2006-01-02T15:04")}})
	if st != http.StatusSeeOther {
		t.Fatalf("task: %d", st)
	}
	do(boss, "POST", base+"/protocol", url.Values{"text": {"Все молодцы"}})
	do(boss, "POST", base+"/status", url.Values{"status": {"done"}})
	m, err = srv.Company.Meeting(ctx, boss, false, mid)
	must(err)
	if m.Status != company.MeetingDone || m.Started == nil || m.Finished == nil || m.Protocol != "Все молодцы" ||
		m.Items[0].Result == "" || len(m.Items[0].Tasks) != 1 || m.Items[0].Tasks[0].Responsible.ID != member {
		t.Fatalf("finished: %+v", m)
	}
	var desc string
	must(pool.QueryRow(ctx, `SELECT description FROM tasks WHERE id::text = $1`, m.Items[0].Tasks[0].ID).Scan(&desc))
	if !strings.Contains(desc, "Планёрка продаж") || !strings.Contains(desc, base) {
		t.Fatalf("task description: %q", desc)
	}
	do(clerk, "POST", base+"/items", url.Values{"title": {"Поздно"}})
	if _, err := srv.Company.AddMeetingItem(ctx, clerk, false, mid, company.MeetingItem{Title: "Поздно"}); err == nil {
		t.Fatal("an item added to a finished meeting")
	}
	if _, body, _ := do(member, "GET", base+"?print=1", nil); !strings.Contains(body, "Дожать сделку") || !strings.Contains(body, "Все молодцы") ||
		!strings.Contains(body, "Позвонить в «Ромашку»") {
		t.Fatal("the minutes to print miss the decisions")
	}

	// the next meeting: a week later, the items not discussed, the open tasks of this one
	st, _, loc = do(boss, "POST", base+"/next", url.Values{})
	if st != http.StatusSeeOther {
		t.Fatalf("next: %d", st)
	}
	nid, _ := strconv.ParseInt(strings.TrimPrefix(loc, "/worktime/meetings/"), 10, 64)
	next, err := srv.Company.Meeting(ctx, member, false, nid)
	must(err)
	if next.PrevID != mid || !next.Start.Equal(start.AddDate(0, 0, 7)) || len(next.Members) != 1 || next.Keeper.ID != clerk || next.EventID == "" {
		t.Fatalf("next: %+v", next)
	}
	var titles []string
	for _, i := range next.Items {
		titles = append(titles, i.Title)
	}
	if strings.Join(titles, ",") != "План на неделю" || len(next.Carried) != 1 || next.Carried[0].Title != "Позвонить в «Ромашку»" {
		t.Fatalf("next agenda %v, carried %+v", titles, next.Carried)
	}
	if _, body, _ := do(member, "GET", "/worktime/meetings/"+strconv.FormatInt(nid, 10), nil); !strings.Contains(body, "Позвонить в «Ромашку»") {
		t.Fatal("the next meeting does not show the open task")
	}

	// REST
	var made apiMeetingOut
	if st := api(bossKey, "POST", "/api/v1/meetings", `{"title":"Совет директоров","start":"`+start.Format(time.RFC3339)+`","members":["`+member+`"]}`, &made); st != http.StatusCreated ||
		made.Minutes != 60 || len(made.Members) != 1 || made.EventID == "" {
		t.Fatalf("api create: %d %+v", st, made)
	}
	path := "/api/v1/meetings/" + strconv.FormatInt(made.ID, 10)
	if st := api(memberKey, "PATCH", path, `{"title":"Моё"}`, nil); st != http.StatusForbidden {
		t.Fatalf("api: a member changes: %d", st)
	}
	var got apiMeetingOut
	if st := api(bossKey, "POST", path+"/items", `{"title":"Бюджет"}`, &got); st != http.StatusCreated || len(got.Items) != 1 {
		t.Fatalf("api item: %d %+v", st, got)
	}
	var tk map[string]string
	if st := api(bossKey, "POST", path+"/items/"+strconv.FormatInt(got.Items[0].ID, 10)+"/tasks", `{"project":"OPS","responsible_id":"`+member+`"}`, &tk); st != http.StatusCreated || tk["key"] == "" {
		t.Fatalf("api task: %d %v", st, tk)
	}
	if st := api(bossKey, "PATCH", path, `{"status":"action","protocol":"Утвердили","members":[]}`, &got); st != http.StatusOK ||
		got.Status != "action" || got.Protocol != "Утвердили" || len(got.Members) != 0 || len(got.Items[0].Tasks) != 1 {
		t.Fatalf("api patch: %d %+v", st, got)
	}
	var list []apiMeetingOut
	if st := api(memberKey, "GET", "/api/v1/meetings", "", &list); st != http.StatusOK || len(list) != 2 {
		t.Fatalf("api list of a member: %d %d", st, len(list)) // the two of the series, not the board's (taken out)
	}
	if st := api(bossKey, "DELETE", path, "", nil); st != http.StatusNoContent {
		t.Fatalf("api delete: %d", st)
	}
	if _, err := cal.Get(ctx, boss, made.EventID); err == nil {
		t.Fatal("the deleted meeting stays in the calendar")
	}
}
