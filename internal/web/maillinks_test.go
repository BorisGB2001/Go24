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
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestMailLinksHTTP drives the mail's links over HTTP against the test database: an invitation answered from the
// card (in the calendar, the overlap shown), a task made of a letter (prefilled, linked both ways), a deal of a
// letter (with the sender's contact, its timeline taking the later letters of the conversation, unlinked), a letter
// linked to a card found by the search, the sender's card, a meeting with people from outside, and a shared
// mailbox of a department — the department reads it, a person sends, a guest and a stranger see nothing.
func TestMailLinksHTTP(t *testing.T) {
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
	user := func(sub, name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $2, $1 || '@go24.test', $3) RETURNING id`, sub, name, guest).Scan(&id))
		return id
	}
	anna, eve, petr, gina := user("anna", "Анна", false), user("eve", "Ева", false), user("petr", "Пётр", false), user("gina", "Гостья", true)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, CRM: crm.NewStore(pool), Mail: mail.NewStore(pool, "x"), Calendar: calendar.NewStore(pool), Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var s3 *storage.Store // the letter's files copied to its task need the S3 storage: make test-db starts a temporary one
	if ep := os.Getenv("GO24_TEST_S3"); ep != "" {
		s3, err = storage.New(ctx, ep, "go24testkey", "go24testsecret", "go24test", false)
		must(err)
		srv.Storages = storages.NewStore(pool, nil, s3)
		srv.Mail.Open = srv.Storages.Open
	}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox, sent string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'INBOX', 'inbox', true) RETURNING id::text`, box).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'Sent', 'sent', true) RETURNING id::text`, box).Scan(&sent))
	uid := 0
	letter := func(folder, from, name, subject, body, thread, ics string, at time.Time) string {
		uid++
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, cc_addr, subject, body, sent_at, thread, ics)
			VALUES ($1, $2, $3, $4, $5, 'anna@go24.test', 'eve@go24.test, boss@client.test', $6, $7, $8, $9, $10) RETURNING id::text`,
			box, folder, uid, from, name, subject, body, at, thread, ics).Scan(&id))
		return id
	}
	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	inv := mail.Invite{UID: "plan-1", Summary: "Планёрка", Location: "Зал", Start: start, End: start.Add(time.Hour),
		Organizer: mail.Person{Email: "ivan@client.test", Name: "Иван"}, Attendees: []mail.Person{{Email: "anna@go24.test", RSVP: true}}}
	invite := letter(inbox, "ivan@client.test", "Иван", "Планёрка", "Приходите", "plan", mail.BuildICS(inv, "REQUEST", time.Now()), time.Now().Add(-time.Hour))
	order := letter(inbox, "ivan@client.test", "Иван Петров", "Заказ на 10 коробок", "Нужно 10 коробок до пятницы", "order@client.test", "", time.Now().Add(-2*time.Hour))
	if _, err := ts.CreateProject(ctx, anna, "GO", "Проект", ""); err != nil {
		t.Fatal(err)
	}

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve, petr, gina} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if want != 0 && res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.800s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	redirect := func(raw string) string {
		t.Helper()
		var r map[string]string
		if err := json.Unmarshal([]byte(raw), &r); err != nil || r["redirect"] == "" {
			t.Fatalf("no redirect: %.500s", raw)
		}
		return r["redirect"]
	}
	read := func(letter string) string { return do(anna, "GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200) }

	// the invitation: the card, «Принять» — the meeting in the calendar; an overlapping meeting is shown
	if p := read(invite); !strings.Contains(p, "data-invite") || !strings.Contains(p, `value="tentative"`) || !strings.Contains(p, "Зал") {
		t.Fatalf("no invitation card: %.3000s", p)
	}
	_, err = srv.Calendar.Save(ctx, anna, calendar.Event{Title: "Созвон", Start: start.Add(30 * time.Minute), End: start.Add(90 * time.Minute)}, nil)
	must(err)
	if p := read(invite); !strings.Contains(p, "Созвон") {
		t.Fatal("the overlap is not shown")
	}
	redirect(do(anna, "POST", "/mail/letters/"+invite+"/invite", url.Values{"status": {"accepted"}, "reply": {"none"}, "folder": {inbox}}, 200))
	st, _ := srv.Mail.InviteAnswer(ctx, anna, "plan-1")
	if e, err := srv.Calendar.Get(ctx, anna, st.EventID); err != nil || e.Title != "Планёрка" || !e.Start.Equal(start) {
		t.Fatalf("meeting = %+v %v", e, err)
	}
	do(eve, "POST", "/mail/letters/"+invite+"/invite", url.Values{"status": {"accepted"}, "reply": {"none"}}, 404)
	if p := read(invite); !strings.Contains(p, "/mail/letters/"+invite+"/propose") || !strings.Contains(p, `name="start" value="`+start.Format("2006-01-02T15:04")) {
		t.Fatal("no «Предложить новое время» on the card")
	}

	// a proposal of a new time for Anna's own meeting: «Принять предложение» moves it
	own, err := srv.Calendar.Save(ctx, anna, calendar.Event{Title: "Обзор", Start: start.Add(24 * time.Hour), End: start.Add(25 * time.Hour)}, nil)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE events SET ical_uid = 'own-1' WHERE id = $1`, own)
	must(err)
	newStart := start.Add(26 * time.Hour)
	ctr := mail.Counter(mail.Invite{UID: "own-1", Summary: "Обзор", Organizer: mail.Person{Email: "anna@go24.test"}},
		mail.Person{Email: "ivan@client.test", Name: "Иван"}, "tentative", "Можно позже?", newStart, newStart.Add(time.Hour))
	counter := letter(inbox, "ivan@client.test", "Иван", "Предлагается новое время: Обзор", "", "own", mail.BuildICS(ctr, "COUNTER", time.Now()), time.Now().Add(-time.Minute))
	if p := read(counter); !strings.Contains(p, "/mail/letters/"+counter+"/counter") || !strings.Contains(p, `value="accepted"`) || !strings.Contains(p, "Можно позже?") {
		t.Fatalf("no proposal card: %.3000s", p)
	}
	do(eve, "POST", "/mail/letters/"+counter+"/counter", url.Values{"decision": {"accepted"}}, 404)
	redirect(do(anna, "POST", "/mail/letters/"+counter+"/counter", url.Values{"decision": {"accepted"}, "folder": {inbox}}, 200))
	if e, err := srv.Calendar.Get(ctx, anna, own); err != nil || !e.Start.Equal(newStart) || !e.End.Equal(newStart.Add(time.Hour)) {
		t.Fatalf("moved meeting = %+v %v", e, err)
	}
	if p := read(counter); strings.Contains(p, "/mail/letters/"+counter+"/counter") {
		t.Fatal("decided proposal still asks")
	}

	// a task of the letter: the form filled, the task linked both ways
	if f := do(anna, "GET", "/tasks/new?mail="+order, nil, 200); !strings.Contains(f, `name="mail" value="`+order) || !strings.Contains(f, "Заказ на 10 коробок") ||
		!strings.Contains(f, "Нужно 10 коробок") {
		t.Fatalf("task form: %.3000s", f)
	}
	if f := do(eve, "GET", "/tasks/new?mail="+order, nil, 200); strings.Contains(f, `name="mail"`) || strings.Contains(f, "Заказ на 10") {
		t.Fatal("someone else's letter fills a task")
	}
	spec := []byte("10 коробок, 40×30×20")
	if s3 != nil { // the letter's file: in the storage, as a fetched letter keeps it
		must(s3.Put(ctx, "mail/test/spec.txt", bytes.NewReader(spec), int64(len(spec)), "text/plain"))
		_, err := pool.Exec(ctx, `INSERT INTO files (object_key, name, content_type, size, mail_id) VALUES ('mail/test/spec.txt', 'spec.txt', 'text/plain', $1, $2)`,
			len(spec), order)
		must(err)
	}
	loc := redirect(do(anna, "POST", "/tasks/new", url.Values{"project": {"GO"}, "title": {"Заказ на 10 коробок"}, "responsible": {anna}, "mail": {order}, "priority": {"1"}}, 200))
	key := strings.TrimPrefix(loc, "/t/")
	if s3 != nil { // the task has a copy of the file of its own, and its history says so
		var fileKey string
		must(pool.QueryRow(ctx, `SELECT f.object_key FROM files f JOIN tasks t ON t.id = f.task_id JOIN projects p ON p.id = t.project_id
			WHERE p.key || '-' || t.number = $1 AND f.name = 'spec.txt'`, key).Scan(&fileKey))
		r, err := s3.Get(ctx, fileKey)
		must(err)
		got, _ := io.ReadAll(r)
		r.Close()
		if fileKey == "mail/test/spec.txt" || !bytes.Equal(got, spec) {
			t.Fatalf("the task's file %s = %q", fileKey, got)
		}
	} else {
		t.Log("no GO24_TEST_S3: the letter's files to its task are not checked")
	}
	if tp := do(anna, "GET", loc, nil, 200); !strings.Contains(tp, "/mail?letter="+order) {
		t.Fatalf("the task does not lead to the letter: %s", key)
	}
	if p := read(order); !strings.Contains(p, "/t/"+key) {
		t.Fatal("the letter does not show its task")
	}

	// a deal of the letter: the sender's contact is made, both have the letter; a later letter joins the deal
	loc = redirect(do(anna, "POST", "/mail/letters/"+order+"/crm", url.Values{"kind": {"deal"}, "folder": {inbox}}, 200))
	if !strings.HasPrefix(loc, "/crm/deals/") || !strings.HasSuffix(loc, "?edit=1") {
		t.Fatalf("deal = %s", loc)
	}
	deal := strings.TrimSuffix(strings.TrimPrefix(loc, "/crm/deals/"), "?edit=1")
	d, err := srv.CRM.Deal(ctx, deal)
	must(err)
	if d.Title != "Заказ на 10 коробок" || d.Contact.Name != "Иван Петров" {
		t.Fatalf("deal = %+v", d)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_mail WHERE message_id = $1`, order).Scan(&n))
	if n != 2 { // the deal's and the contact's
		t.Fatalf("the letter is in %d cards", n)
	}
	letter(sent, "anna@go24.test", "Анна", "Re: Заказ на 10 коробок", "Отгрузим в четверг", "order@client.test", "", time.Now().Add(-time.Hour))
	card := do(anna, "GET", "/crm/deals/"+deal, nil, 200)
	if !strings.Contains(card, "Нужно 10 коробок") || !strings.Contains(card, "Отгрузим в четверг") || !strings.Contains(card, "/mail?letter="+order) {
		i := strings.Index(card, `id="timeline"`)
		card = card[max(i, 0):]
		t.Fatalf("the deal's timeline: %.4000s", card)
	}
	if c := do(eve, "GET", "/crm/deals/"+deal, nil, 200); strings.Contains(c, "/mail?letter="+order) {
		t.Fatal("someone else is led to anna's mailbox")
	}
	if p := read(order); !strings.Contains(p, "/crm/deals/"+deal) || !strings.Contains(p, "Иван Петров") {
		t.Fatal("the letter does not show its deal and the sender's card")
	}
	var link string
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_mail WHERE entity = 'deal' AND entity_id = $1 AND message_id = $2`, deal, order).Scan(&link))
	redirect(do(anna, "POST", "/crm/deals/"+deal+"/mail/"+link+"/unlink", url.Values{}, 200))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_mail WHERE entity = 'deal' AND entity_id = $1`, deal).Scan(&n))
	if n != 0 {
		t.Fatalf("unlinked conversation left %d letters", n)
	}
	// the search finds the deal; the letter is linked to it again
	var found []map[string]string
	must(json.Unmarshal([]byte(do(anna, "GET", "/mail/crm/search?q="+url.QueryEscape("коробок"), nil, 200)), &found))
	if len(found) == 0 || found[0]["path"] != "/crm/deals/"+deal {
		t.Fatalf("search = %v", found)
	}
	redirect(do(anna, "POST", "/mail/letters/"+order+"/crm", url.Values{"target": {"/crm/deals/" + deal}}, 200))
	do(anna, "POST", "/mail/letters/"+order+"/crm", url.Values{"target": {"https://evil.test/crm/deals/1"}}, 422)
	do(eve, "POST", "/mail/letters/"+order+"/crm", url.Values{"kind": {"lead"}}, 404)
	do(anna, "GET", "/crm/deals/"+deal, nil, 200) // the answer comes along again
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_mail WHERE entity = 'deal' AND entity_id = $1`, deal).Scan(&n))
	if n != 2 {
		t.Fatalf("linked again: %d", n)
	}
	// a lead (the old way too) is in its timeline
	loc = redirect(do(anna, "POST", "/mail/letters/"+invite+"/lead", url.Values{}, 200))
	if l := do(anna, "GET", loc, nil, 200); !strings.Contains(l, "Приходите") {
		t.Fatal("the lead's timeline has no letter")
	}

	// a meeting of a letter: go24's people invited, the others by mail (no mail server here: said, but kept)
	if f := do(anna, "GET", "/calendar/new?mail="+order+"&reply=1", nil, 200); !strings.Contains(f, "ivan@client.test, boss@client.test") ||
		!strings.Contains(f, `name="reply_to" value="`+order) || !strings.Contains(f, `value="`+eve+`"`) {
		t.Fatalf("meeting form: %.4000s", f)
	}
	loc = redirect(do(anna, "POST", "/calendar/new", url.Values{"title": {"Обсуждение"}, "start": {start.Add(24 * time.Hour).Format("2006-01-02T15:04")},
		"end": {start.Add(25 * time.Hour).Format("2006-01-02T15:04")}, "external": {"ivan@client.test, Boss <boss@client.test>"}, "mailbox": {box}, "invite": {eve}}, 200))
	ev := loc[strings.LastIndex(loc, "#")+1:]
	ext, err := srv.Calendar.Externals(ctx, ev)
	must(err)
	mi, _ := srv.Calendar.Mail(ctx, ev)
	if len(ext) != 2 || ext[0].Email != "boss@client.test" || ext[0].Name != "Boss" || mi.UID == "" || mi.MailboxID != box {
		t.Fatalf("externals = %+v %+v", ext, mi)
	}
	if e := do(anna, "GET", "/calendar/"+ev, nil, 200); !strings.Contains(e, "boss@client.test") {
		t.Fatal("the meeting does not show its people from outside")
	}

	// a shared mailbox: nobody else sees it until shared with the department
	do(eve, "GET", "/mail/shared/"+box, nil, 404)
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&dept))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Продажи', $1) RETURNING id`, dept).Scan(&dept))
	for _, u := range []string{eve, gina} {
		_, err := pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, u, dept)
		must(err)
	}
	do(eve, "POST", "/mail/shared", url.Values{"box": {box}, "department": {dept}, "dept_level": {"manage"}}, 404)
	redirect(do(anna, "POST", "/mail/shared", url.Values{"box": {box}, "name": {"Отдел продаж"}, "department": {dept}, "dept_level": {"read"}}, 200))
	if p := do(eve, "GET", "/mail/shared/"+box, nil, 200); !strings.Contains(p, "Заказ на 10 коробок") || strings.Contains(p, "/assign") {
		t.Fatalf("the department's view: %.3000s", p)
	}
	if p := do(eve, "GET", "/mail/shared/"+box+"?letter="+order, nil, 200); !strings.Contains(p, "Нужно 10 коробок") || strings.Contains(p, "/reply") {
		t.Fatal("a reader does not read the conversation, or may answer")
	}
	if p := do(eve, "GET", "/mail", nil, 200); !strings.Contains(p, "/mail/shared/"+box) {
		t.Fatal("the shared mailbox is not in the folder pane")
	}
	do(petr, "GET", "/mail/shared/"+box, nil, 404)
	do(petr, "GET", "/mail/shared/"+box+"?letter="+order, nil, 404)
	if code := status(t, client, hs.URL+"/mail/shared/"+box, as[gina]); code == 200 {
		t.Fatal("a guest opens the shared mailbox")
	}
	do(eve, "POST", "/mail/shared/"+box+"/letters/"+order+"/assign", url.Values{"user": {eve}}, 403)
	redirect(do(anna, "POST", "/mail/shared/"+box+"/people", url.Values{"user": {petr}, "level": {"send"}}, 200))
	do(anna, "POST", "/mail/shared/"+box+"/people", url.Values{"user": {gina}, "level": {"read"}}, 422)
	redirect(do(petr, "POST", "/mail/shared/"+box+"/letters/"+order+"/assign", url.Values{"user": {eve}}, 200))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'mail_assign'`, eve).Scan(&n))
	if n != 1 {
		t.Fatalf("the assigned is not told: %d", n)
	}
	redirect(do(petr, "POST", "/mail/shared/"+box+"/letters/"+order+"/status", url.Values{"status": {"done"}}, 200))
	if p := do(eve, "GET", "/mail/shared/"+box+"?status=done&who=me", nil, 200); !strings.Contains(p, "Заказ на 10 коробок") {
		t.Fatal("the done request is not hers")
	}
	if p := do(eve, "GET", "/mail/shared/"+box, nil, 200); strings.Contains(p, "Заказ на 10 коробок") {
		t.Fatal("a done request is among the open ones")
	}
	if p := do(petr, "GET", "/mail/shared/"+box+"?letter="+order, nil, 200); !strings.Contains(p, "/reply") || !strings.Contains(p, "Отдел продаж") {
		t.Fatal("one who sends cannot answer as the mailbox")
	}
	if !srv.Mail.SharedCan(ctx, eve, order) || srv.Mail.SharedCan(ctx, gina, order) {
		t.Fatal("the files of the shared letters")
	}
	do(eve, "GET", "/mail/shared/"+box+"?tab=access", nil, 200)

	// the REST API
	api := func(who, method, path, body string, want int) string {
		t.Helper()
		tok, err := srv.Keys.Create(ctx, who, "k", []string{"mail:read", "mail:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
		}
		return string(raw)
	}
	var links struct {
		Invite map[string]any   `json:"invite"`
		Tasks  []map[string]any `json:"tasks"`
		CRM    []map[string]any `json:"crm"`
	}
	must(json.Unmarshal([]byte(api(anna, "GET", "/api/v1/mail/messages/"+order+"/links", "", 200)), &links))
	if links.Invite != nil || len(links.Tasks) != 1 || links.Tasks[0]["key"] != key || len(links.CRM) == 0 {
		t.Fatalf("links = %+v", links)
	}
	must(json.Unmarshal([]byte(api(anna, "GET", "/api/v1/mail/messages/"+invite+"/links", "", 200)), &links))
	if links.Invite == nil || links.Invite["answer"] != "accepted" || links.Invite["uid"] != "plan-1" {
		t.Fatalf("invite = %+v", links.Invite)
	}
	if c, _ := links.Invite["counter"].(map[string]any); c != nil {
		t.Fatalf("a counter on an invitation: %+v", c)
	}
	var cl struct {
		Invite map[string]any `json:"invite"`
	}
	must(json.Unmarshal([]byte(api(anna, "GET", "/api/v1/mail/messages/"+counter+"/links", "", 200)), &cl))
	if c, _ := cl.Invite["counter"].(map[string]any); c == nil || c["decision"] != "accepted" || c["from"] != "ivan@client.test" || c["meeting"] == nil {
		t.Fatalf("counter = %+v", cl.Invite)
	}
	api(anna, "POST", "/api/v1/mail/messages/"+counter+"/counter", `{"decision": "maybe"}`, 422)
	api(anna, "POST", "/api/v1/mail/messages/"+invite+"/invite", `{"status": "declined", "reply": "none"}`, 200)
	if st, _ := srv.Mail.InviteAnswer(ctx, anna, "plan-1"); st.Status != "declined" || st.EventID != "" {
		t.Fatalf("declined = %+v", st)
	}
	if out := api(anna, "POST", "/api/v1/mail/messages/"+order+"/crm", `{"kind": "contact"}`, 200); !strings.Contains(out, `"/crm/contacts/`) {
		t.Fatalf("contact = %s", out)
	}
	api(eve, "GET", "/api/v1/mail/messages/"+order+"/links", "", 404)
	if out := api(eve, "GET", "/api/v1/mail/shared", "", 200); !strings.Contains(out, "Отдел продаж") {
		t.Fatalf("shared = %s", out)
	}
	if out := api(eve, "GET", "/api/v1/mail/shared/"+box+"/tickets?status=done", "", 200); !strings.Contains(out, "Заказ на 10 коробок") {
		t.Fatalf("tickets = %s", out)
	}
	api(petr, "POST", "/api/v1/mail/shared/"+box+"/letters/"+order+"/status", `{"status": "progress"}`, 200)
	api(eve, "POST", "/api/v1/mail/shared/"+box+"/letters/"+order+"/status", `{"status": "done"}`, 403)
	redirect(do(anna, "POST", "/mail/shared/"+box+"/unshare", url.Values{}, 200))
	do(eve, "GET", "/mail/shared/"+box, nil, 404)
}

// status is the answer's code to a GET of the person (without following).
func status(t *testing.T, c *http.Client, u string, cookie *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	req.AddCookie(cookie)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}
