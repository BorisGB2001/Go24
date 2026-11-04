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
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailPollHTTP: «Опрос о времени встречи» — the editor's button and dialog, the busy time of a colleague and
// «Подобрать время», the poll made from To and Cc with its block for the letter, the people's page (who are you, the
// answers, an employee signed in answers for themself), the organizer's page and notifications, the meeting made
// by itself when everyone agreed (the employee invited, the client among the people from outside), a closed poll;
// «Назначить» by hand and the REST methods; another's poll is not found.
func TestMailPollHTTP(t *testing.T) {
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
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), Calendar: calendar.NewStore(pool), I18n: bundle, Keys: keys,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BaseURL: "http://go24.test"}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	_ = box

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), eve: cookie(eve)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var lastLoc string
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if who != "" {
			req.AddCookie(as[who])
		}
		if method == "POST" && strings.HasPrefix(path, "/mail/polls") && !strings.Contains(path, "/schedule") && !strings.Contains(path, "/cancel") && !strings.Contains(path, "/delete") {
			req.Header.Set("X-Go24-Form", "1")
			req.Header.Set("Accept", "application/json")
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		lastLoc = res.Header.Get("Location")
		return string(raw)
	}

	page := do(anna, "GET", "/mail?new=1", nil, 200)
	for _, want := range []string{"data-poll ", "data-poll-dlg", `data-p="auto"`, "js/mailpoll.js", `href="/mail/polls"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the editor has no %s", want)
		}
	}

	// Eve is busy tomorrow at 10:00
	day := time.Now().AddDate(0, 0, 2)
	at := func(h int) time.Time { return time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, time.Local) }
	_, err = srv.Calendar.Save(ctx, eve, calendar.Event{Title: "Занята", Start: at(10), End: at(11), Accessibility: "busy"}, nil)
	must(err)
	f := func(t time.Time) string { return t.Format("2006-01-02T15:04") }
	var chk struct {
		Options []struct {
			Start string
			Busy  []string
		}
		Suggest []string
		People  int
	}
	must(json.Unmarshal([]byte(do(anna, "POST", "/mail/polls/check", url.Values{"addr": {"Ева <eve@go24.test>, client@out.test"}, "start": {f(at(10)), f(at(12))},
		"length": {"30"}, "suggest": {"1"}}, 200)), &chk))
	if chk.People != 2 || len(chk.Options) != 2 || len(chk.Options[0].Busy) != 1 || chk.Options[0].Busy[0] != "Ева" || len(chk.Options[1].Busy) != 0 || len(chk.Suggest) == 0 {
		t.Errorf("check = %+v", chk)
	}

	do(anna, "POST", "/mail/polls", url.Values{"title": {"Планёрка"}, "length": {"30"}, "to": {""}, "start": {f(at(12))}}, 422)
	var made struct{ ID, URL, Page, HTML string }
	must(json.Unmarshal([]byte(do(anna, "POST", "/mail/polls", url.Values{"title": {"Планёрка"}, "length": {"30"}, "location": {"Переговорная"},
		"video": {"1"}, "auto": {"1"}, "box": {box}, "to": {"Ева <eve@go24.test>"}, "cc": {"Клиент <client@out.test>"},
		"start": {f(at(12)), f(at(14))}}, 200)), &made))
	tok := strings.TrimPrefix(made.URL, "http://go24.test/mp/")
	if made.ID == "" || tok == made.URL || !strings.Contains(made.HTML, `href="`+made.URL+`"`) || !strings.Contains(made.HTML, "Планёрка") ||
		!strings.Contains(made.HTML, at(12).Format("02.01.2006 15:04")) {
		t.Fatalf("made = %+v", made)
	}

	// the people's page: who are you, then the answers
	page = do("", "GET", "/mp/"+tok, nil, 200)
	if !strings.Contains(page, "Кто вы?") && !strings.Contains(page, "Who are you?") {
		t.Errorf("no «who are you»: %.500s", page)
	}
	p, err := srv.Mail.Poll(ctx, anna, made.ID)
	must(err)
	var ev, cl string
	for _, v := range p.Voters {
		if v.UserID == eve {
			ev = v.ID
		} else {
			cl = v.ID
		}
	}
	o1, o2 := p.Options[0].ID, p.Options[1].ID
	page = do("", "GET", "/mp/"+tok+"?v="+cl, nil, 200)
	if !strings.Contains(page, `name="a_`+o1+`"`) || !strings.Contains(page, `value="`+cl+`"`) {
		t.Error("the answers form is not there")
	}
	// Eve signed in answers for herself, whoever the form names
	do(eve, "POST", "/mp/"+tok, url.Values{"voter": {cl}, "a_" + o1: {"yes"}, "a_" + o2: {"maybe"}}, http.StatusSeeOther)
	if !strings.Contains(lastLoc, "v="+ev) || !strings.Contains(lastLoc, "done=1") {
		t.Errorf("after the vote: %s", lastLoc)
	}
	p, _ = srv.Mail.Poll(ctx, anna, made.ID)
	if v, _ := p.Voter(ev); v.Voted == nil {
		t.Error("Eve has not voted")
	}
	if v, _ := p.Voter(cl); v.Voted != nil {
		t.Error("the client voted by Eve's hand")
	}
	page = do(anna, "GET", "/mail/polls/"+made.ID, nil, 200)
	if !strings.Contains(page, "mpoll-grid") || !strings.Contains(page, made.URL) || !strings.Contains(page, `/mail/polls/`+made.ID+`/schedule`) {
		t.Error("the organizer's page has no grid, link or «Назначить»")
	}
	do(eve, "GET", "/mail/polls/"+made.ID, nil, 404)
	if page = do(anna, "GET", "/mail/polls", nil, 200); !strings.Contains(page, "Планёрка") {
		t.Error("the poll is not in the list")
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'mail_poll'`, anna).Scan(&n))
	if n != 1 {
		t.Errorf("%d notifications of votes", n)
	}

	// the client agrees to the first time too: the meeting is made by itself
	do("", "POST", "/mp/"+tok, url.Values{"voter": {cl}, "a_" + o1: {"yes"}}, http.StatusSeeOther)
	p, _ = srv.Mail.Poll(ctx, anna, made.ID)
	if p.Status != mail.PollScheduled || p.ChosenID != o1 || p.EventID == "" {
		t.Fatalf("not scheduled by itself: %+v", p)
	}
	e, err := srv.Calendar.Get(ctx, anna, p.EventID)
	must(err)
	if e.Title != "Планёрка" || !e.Start.Equal(at(12)) || !e.End.Equal(at(12).Add(30*time.Minute)) || !e.Video || e.Where != "Переговорная" ||
		len(e.Attendees) != 1 || e.Attendees[0].ID != eve {
		t.Errorf("the meeting: %+v", e)
	}
	ext, err := srv.Calendar.Externals(ctx, p.EventID)
	must(err)
	if len(ext) != 1 || ext[0].Email != "client@out.test" {
		t.Errorf("people from outside: %+v", ext)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'mail_poll_done'`, anna).Scan(&n))
	if n != 1 {
		t.Errorf("%d notifications of the meeting", n)
	}
	do("", "POST", "/mp/"+tok, url.Values{"voter": {cl}, "a_" + o2: {"yes"}}, http.StatusSeeOther)
	if !strings.Contains(lastLoc, "msg=") {
		t.Error("a vote in a closed poll is taken")
	}
	if page = do("", "GET", "/mp/"+tok, nil, 200); !strings.Contains(page, "bk-done") {
		t.Error("the people's page of a closed poll")
	}
	do(anna, "POST", "/mail/polls/"+made.ID+"/schedule", url.Values{"option": {o2}}, http.StatusSeeOther)
	if !strings.Contains(lastLoc, "msg=") {
		t.Error("a second meeting")
	}
	do("", "GET", "/mp/nope", nil, 404)

	// REST: a poll without «auto», scheduled by hand
	api := func(who, method, path, body string, want int) map[string]any {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"mail:read", "mail:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, b)
		}
		out := map[string]any{}
		_ = json.Unmarshal(b, &out)
		return out
	}
	in, _ := json.Marshal(map[string]any{"title": "Обед", "length": 60, "people": []string{"eve@go24.test"}, "starts": []time.Time{at(13)}})
	out := api(anna, "POST", "/api/v1/mail/polls", string(in), 201)
	id, _ := out["id"].(string)
	opts, _ := out["options"].([]any)
	if id == "" || out["status"] != "open" || len(opts) != 1 || !strings.Contains(out["html"].(string), "Обед") {
		t.Fatalf("POST poll = %v", out)
	}
	opt := opts[0].(map[string]any)["id"].(string)
	api(eve, "GET", "/api/v1/mail/polls/"+id, "", 404)
	if out = api(anna, "POST", "/api/v1/mail/polls/"+id+"/schedule", `{"option":"`+opt+`"}`, 200); out["status"] != "scheduled" || out["event"] == "" {
		t.Errorf("schedule = %v", out)
	}
	api(anna, "POST", "/api/v1/mail/polls/"+id+"/cancel", "", 422)
	var list []map[string]any
	{
		k, err := keys.Create(ctx, anna, "k", []string{"mail:read"}, time.Hour)
		must(err)
		req, _ := http.NewRequest("GET", hs.URL+"/api/v1/mail/polls", nil)
		req.Header.Set("Authorization", "Bearer "+k)
		res, err := http.DefaultClient.Do(req)
		must(err)
		must(json.NewDecoder(res.Body).Decode(&list))
		res.Body.Close()
	}
	if len(list) != 2 {
		t.Errorf("%d polls", len(list))
	}
	api(anna, "DELETE", "/api/v1/mail/polls/"+id, "", 204)
	do(anna, "POST", "/mail/polls/"+made.ID+"/delete", url.Values{}, http.StatusSeeOther)
	if _, err := srv.Mail.Poll(ctx, anna, made.ID); err == nil {
		t.Error("the poll is not deleted")
	}
}
