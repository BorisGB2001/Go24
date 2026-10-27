package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
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
	"go24/internal/marketing"
	"go24/internal/storages"
	"go24/internal/tasks"
)

func TestMarketingHTTP(t *testing.T) {
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
	var anna, boris string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'Борис', 'boris@t') RETURNING id`).Scan(&boris))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool), Mail: mail.NewStore(pool, "test-secret"),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		BaseURL: "http://go24.test/"}
	srv.Marketing = marketing.NewStore(pool, srv.CRM)
	port, letters := fakeSMTP(t)
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@shop.test", IMAPHost: "127.0.0.1", IMAPPort: 1, SMTPHost: "127.0.0.1", SMTPPort: port, Password: "pw"})
	must(err)
	for _, c := range []crm.Contact{{Name: "Иван", Email: "ivan@client.test"}, {Name: "Пётр", Email: "petr@client.test"},
		{Name: "Мария", Email: "maria@client.test"}, {Name: "Олег"}} {
		c.Responsible = crm.Ref{ID: anna}
		_, err := srv.CRM.SaveContact(ctx, anna, c)
		must(err)
	}

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(user string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: user}))
		return rec.Result().Cookies()[0]
	}
	annaC, borisC := cookieOf(anna), cookieOf(boris)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(c *http.Cookie, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if c != nil {
			req.AddCookie(c)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(b)
	}

	// segments: the CRM contacts with an address, a list, and a list to exclude
	if code, loc, _ := do(annaC, "POST", "/marketing/segments/new", url.Values{"name": {"Все контакты"}, "entity": {"contact"}}); code != http.StatusSeeOther || !strings.Contains(loc, "#seg-") {
		t.Fatalf("segment: %d %s", code, loc)
	}
	do(annaC, "POST", "/marketing/segments/new", url.Values{"name": {"Выставка"}, "entity": {"list"}, "emails": {"Икс <x@list.test>\nmaria@client.test;Мария\nне адрес"}})
	do(annaC, "POST", "/marketing/segments/new", url.Values{"name": {"Не писать"}, "entity": {"list"}, "emails": {"petr@client.test"}})
	if _, loc, _ := do(annaC, "POST", "/marketing/segments/new", url.Values{"name": {"Пустой"}, "entity": {"list"}, "emails": {"нет"}}); !strings.Contains(loc, "edit=new") {
		t.Fatalf("an empty list is kept: %s", loc)
	}
	segs, err := srv.Marketing.Segments(ctx)
	must(err)
	if len(segs) != 3 {
		t.Fatalf("segments: %+v", segs)
	}
	seg := map[string]string{}
	for _, g := range segs {
		seg[g.Name] = g.ID
	}
	if a, err := srv.Marketing.Audience(ctx, anna, marketing.Segment{Entity: "contact"}); err != nil || len(a) != 3 {
		t.Fatalf("the contacts' audience: %+v %v", a, err)
	}
	if a, err := srv.Marketing.Audience(ctx, anna, marketing.Segment{Entity: "contact", Filter: marketing.Filter{Q: "Иван"}}); err != nil || len(a) != 1 || a[0].Email != "ivan@client.test" {
		t.Fatalf("the filtered audience: %+v %v", a, err)
	}
	if code, _, body := do(annaC, "GET", "/marketing/segments", nil); code != http.StatusOK || !strings.Contains(body, "Выставка") {
		t.Fatalf("segments page: %d %s", code, body)
	}

	// a campaign: all contacts and the list but «Не писать»
	form := url.Values{"title": {"Осенняя акция"}, "box": {box}, "subject": {"Скидки для {Name}"},
		"html": {`<p>Здравствуйте, {Name}!</p><p><a href="https://shop.test/sale?a=1&amp;b=2">Акция</a></p>`},
		"seg":  {seg["Все контакты"], seg["Выставка"]}, "exclude": {seg["Не писать"]}}
	code, loc, _ := do(annaC, "POST", "/marketing/campaigns/new", form)
	if code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("campaign: %d %s", code, loc)
	}
	id := strings.TrimPrefix(loc[:strings.Index(loc, "?")], "/marketing/campaigns/")
	if code, _, body := do(annaC, "GET", "/marketing/campaigns/"+id, nil); code != http.StatusOK || !strings.Contains(body, "Осенняя акция") {
		t.Fatalf("campaign page: %d", code)
	}
	// Boris may see it but not change it
	if code, loc, _ := do(borisC, "POST", "/marketing/campaigns/"+id, form); code == http.StatusSeeOther && !strings.Contains(loc, "err") && !strings.Contains(loc, "msg=") {
		t.Fatalf("Boris changes Anna's campaign: %d %s", code, loc)
	}
	if c, _ := srv.Marketing.Campaign(ctx, id); c.CreatedBy != anna || c.MailboxID != box {
		t.Fatalf("the campaign after Boris: %+v", c)
	}
	if code, _, _ := do(borisC, "POST", "/marketing/campaigns/"+id+"/delete", nil); code == http.StatusSeeOther {
		if _, err := srv.Marketing.Campaign(ctx, id); err != nil {
			t.Fatal("Boris deleted Anna's campaign")
		}
	}

	// the test letter
	form.Set("go", "test")
	form.Set("test_to", "anna@other.test")
	if _, loc, _ := do(annaC, "POST", "/marketing/campaigns/"+id, form); !strings.Contains(loc, "msg=") || len(letters()) != 1 {
		t.Fatalf("test letter: %s %d", loc, len(letters()))
	}
	if code, loc, _ := do(nil, "GET", "/m/c/test?u="+url.QueryEscape("https://shop.test/x"), nil); code != http.StatusFound || loc != "https://shop.test/x" {
		t.Fatalf("a test letter's link: %d %s", code, loc)
	}

	// start: Иван, Мария, Икс (Пётр is excluded, Олег has no address)
	form.Set("go", "start")
	if _, loc, _ := do(annaC, "POST", "/marketing/campaigns/"+id, form); !strings.Contains(loc, "msg=") {
		t.Fatalf("start: %s", loc)
	}
	c, err := srv.Marketing.Campaign(ctx, id)
	must(err)
	if c.Status != "sending" || c.Total != 3 {
		t.Fatalf("started: %s %d", c.Status, c.Total)
	}
	if _, err := srv.Marketing.SaveCampaign(ctx, anna, false, c); err == nil {
		t.Fatal("a started campaign is changed")
	}
	// Мария unsubscribes elsewhere meanwhile: the stop list
	if code, _, _ := do(annaC, "POST", "/marketing/stoplist", url.Values{"emails": {"Maria@Client.test"}}); code != http.StatusSeeOther {
		t.Fatalf("stop list: %d", code)
	}
	n, err := srv.Marketing.SendDue(ctx, time.Now(), srv.MarketingSend)
	must(err)
	if n != 2 {
		t.Fatalf("sent %d", n)
	}
	c, _ = srv.Marketing.Campaign(ctx, id)
	if c.Status != "done" || c.Sent != 2 || c.Skipped != 1 {
		t.Fatalf("done: %+v", c)
	}
	got := letters()
	if len(got) != 3 {
		t.Fatalf("letters: %d", len(got))
	}
	list, _, err := srv.Marketing.Recipients(ctx, id, "sent", 10, 0)
	must(err)
	ivan := list[0]
	if ivan.Email != "ivan@client.test" {
		t.Fatalf("recipients: %+v", list)
	}
	raw := got[1]
	if strings.Contains(got[2], "ivan@client.test") {
		raw = got[2]
	}
	b, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(raw)))
	letter := string(b)
	unsub := "http://go24.test/m/u/" + ivan.Token
	for _, want := range []string{"List-Unsubscribe: <" + unsub + ">", "List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"Здравствуйте, Иван!", `src="http://go24.test/m/o/` + ivan.Token + `.gif"`, `href="` + unsub + `"`,
		`href="http://go24.test/m/c/` + ivan.Token + `?u=https%3A%2F%2Fshop.test%2Fsale%3Fa%3D1%26b%3D2"`} {
		if !strings.Contains(letter, want) {
			t.Fatalf("the letter has no %q:\n%s", want, letter)
		}
	}
	if regexp.MustCompile(`href="https://shop`).MatchString(letter) {
		t.Fatal("a link is not counted")
	}

	// opened, clicked; a link not of the letter leads nowhere
	if code, _, body := do(nil, "GET", "/m/o/"+ivan.Token+".gif", nil); code != http.StatusOK || !strings.HasPrefix(body, "GIF89a") {
		t.Fatalf("pixel: %d", code)
	}
	if code, _, _ := do(nil, "GET", "/m/c/"+ivan.Token+"?u="+url.QueryEscape("https://evil.test/"), nil); code != http.StatusNotFound {
		t.Fatalf("a foreign link: %d", code)
	}
	if code, loc, _ := do(nil, "GET", "/m/c/"+ivan.Token+"?u="+url.QueryEscape("https://shop.test/sale?a=1&b=2"), nil); code != http.StatusFound || loc != "https://shop.test/sale?a=1&b=2" {
		t.Fatalf("the letter's link: %d %s", code, loc)
	}
	if code, _, _ := do(nil, "GET", "/m/c/nosuch?u="+url.QueryEscape("https://shop.test/sale?a=1&b=2"), nil); code != http.StatusNotFound {
		t.Fatalf("a wrong token: %d", code)
	}
	c, _ = srv.Marketing.Campaign(ctx, id)
	if c.Opened != 1 || c.Clicked != 1 {
		t.Fatalf("opened %d, clicked %d", c.Opened, c.Clicked)
	}

	// unsubscribe: the page asks, the button puts the address into the stop list; the one-click POST of a mail client
	if code, _, body := do(nil, "GET", "/m/u/"+ivan.Token, nil); code != http.StatusOK || !strings.Contains(body, "ivan@client.test") {
		t.Fatalf("unsubscribe page: %d", code)
	}
	if code, _, _ := do(nil, "POST", "/m/u/"+ivan.Token, url.Values{"List-Unsubscribe": {"One-Click"}}); code != http.StatusOK {
		t.Fatalf("one-click: %d", code)
	}
	stop, err := srv.Marketing.StopList(ctx, "", 10)
	must(err)
	if len(stop) != 2 {
		t.Fatalf("stop list: %+v", stop)
	}
	if c, _ = srv.Marketing.Campaign(ctx, id); c.Unsubscribed != 1 {
		t.Fatalf("unsubscribed %d", c.Unsubscribed)
	}
	// the letter is in the timeline of Иван's card, with what he did; Мария, skipped, got none
	evs, err := srv.CRM.Timeline(ctx, "contact", ivan.EntityID)
	must(err)
	var ev *crm.Event
	for i := range evs {
		if evs[i].Kind == "mailing" {
			ev = &evs[i]
		}
	}
	if ev == nil || ev.Mailing == nil || ev.Mailing.CampaignID != id || ev.Mailing.Email != "ivan@client.test" || ev.AuthorID != anna ||
		ev.Mailing.Opened == nil || ev.Mailing.Clicked == nil || ev.Mailing.Unsubscribed == nil {
		t.Fatalf("Иван's timeline: %+v", ev)
	}
	if code, _, body := do(annaC, "GET", "/crm/contacts/"+ivan.EntityID, nil); code != http.StatusOK ||
		!strings.Contains(body, `href="/marketing/campaigns/`+id+`"`) || !strings.Contains(body, "→ ivan@client.test") {
		t.Fatalf("Иван's card: %d", code)
	}
	var maria int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline t JOIN crm_contacts c ON c.id = t.entity_id
		WHERE c.email = 'maria@client.test' AND t.kind = 'mailing'`).Scan(&maria))
	if maria != 0 {
		t.Fatalf("Мария's timeline: %d", maria)
	}
	if code, _, body := do(nil, "GET", "/m/u/nosuch", nil); code != http.StatusOK || strings.Contains(body, "@") && strings.Contains(body, "ivan") {
		t.Fatalf("a wrong token's page: %d", code)
	}

	// a copy is a draft again; the next campaign skips the stop list at once
	if _, loc, _ := do(annaC, "POST", "/marketing/campaigns/"+id+"/copy", nil); !strings.HasPrefix(loc, "/marketing/campaigns/") {
		t.Fatalf("copy: %s", loc)
	}
	all, err := srv.Marketing.Campaigns(ctx)
	must(err)
	if len(all) != 2 || all[0].Status != "draft" {
		t.Fatalf("campaigns: %+v", all)
	}
	if to, err := srv.Marketing.Addressees(ctx, anna, all[0]); err != nil || len(to) != 1 || to[0].Email != "x@list.test" {
		t.Fatalf("the copy's addressees: %+v %v", to, err)
	}

	// «Отправить повторно»: to those who got it and did not open it — Икс (Иван opened it, Мария was skipped)
	if code, _, body := do(annaC, "GET", "/marketing/campaigns/"+id, nil); code != http.StatusOK || !strings.Contains(body, "/resend-unopened") {
		t.Fatalf("no resend in the report: %d", code)
	}
	if _, loc, _ := do(annaC, "POST", "/marketing/campaigns/"+all[0].ID+"/resend-unopened", nil); !strings.Contains(loc, "msg=") {
		t.Fatalf("a draft is resent: %s", loc)
	}
	_, loc, _ = do(annaC, "POST", "/marketing/campaigns/"+id+"/resend-unopened", nil)
	rid := strings.TrimPrefix(loc, "/marketing/campaigns/")
	re, err := srv.Marketing.Campaign(ctx, rid)
	must(err)
	if re.ResendOf != id || re.ResendTo != "unopened" || re.ResendTitle != c.Title || re.Subject != c.Subject || !re.Editable() || len(re.Segments) != 0 {
		t.Fatalf("the resend: %+v", re)
	}
	if to, err := srv.Marketing.Addressees(ctx, anna, re); err != nil || len(to) != 1 || to[0].Email != "x@list.test" {
		t.Fatalf("the resend's addressees: %+v %v", to, err)
	}
	if code, _, body := do(annaC, "GET", "/marketing/campaigns/"+rid, nil); code != http.StatusOK || !strings.Contains(body, `href="/marketing/campaigns/`+id+`"`) {
		t.Fatalf("the resend's page: %d", code)
	}
	if _, err := srv.Marketing.ResendCampaign(ctx, anna, id, "all", "x"); err == nil {
		t.Fatal("resent to all")
	}
	must(srv.Marketing.DeleteCampaign(ctx, anna, false, rid))

	// the campaign deleted, its line stays in the card, without the letter
	must(srv.Marketing.DeleteCampaign(ctx, anna, false, id))
	evs, err = srv.CRM.Timeline(ctx, "contact", ivan.EntityID)
	must(err)
	for _, e := range evs {
		if e.Kind == "mailing" && e.Mailing != nil {
			t.Fatalf("a deleted campaign's letter: %+v", e.Mailing)
		}
	}
	if code, _, body := do(annaC, "GET", "/crm/contacts/"+ivan.EntityID, nil); code != http.StatusOK ||
		!strings.Contains(body, "рассылка удалена") && !strings.Contains(body, "the campaign is deleted") {
		t.Fatalf("Иван's card, the campaign deleted: %d", code)
	}
	// templates: a campaign from a ready-made one, «Сохранить как шаблон», the author's template, others' read-only
	if code, _, body := do(annaC, "GET", "/marketing/templates", nil); code != http.StatusOK || !strings.Contains(body, `action="/marketing/templates/base-promo/use"`) {
		t.Fatalf("templates page: %d", code)
	}
	code, loc, _ = do(annaC, "POST", "/marketing/templates/base-promo/use", url.Values{})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/marketing/campaigns/") {
		t.Fatalf("use a ready-made template: %d %s", code, loc)
	}
	promo, err := srv.Marketing.Campaign(ctx, strings.TrimPrefix(loc, "/marketing/campaigns/"))
	must(err)
	if promo.Status != "draft" || promo.Subject == "" || promo.MailboxID != box || !strings.Contains(promo.Body, "SALE20") ||
		!strings.Contains(promo.Body, "background-color: #f06a2a") || !strings.Contains(promo.Body, "{Name}") {
		t.Fatalf("the campaign of a template: %+v", promo)
	}
	// the blocks of the editor: columns and social networks keep their layout in the letter
	if code, _, body := do(annaC, "GET", "/marketing/campaigns/"+promo.ID, nil); code != http.StatusOK ||
		!strings.Contains(body, `data-mkt-add="cols"`) || !strings.Contains(body, `data-mkt-soc="telegram"`) {
		t.Fatalf("the editor's blocks: %d", code)
	}
	blocks := `<table width="100%" cellpadding="0" cellspacing="0" border="0" style="table-layout:fixed"><tbody><tr>` +
		`<td width="50%" valign="top" style="padding:0 8px 0 0px"><h3>A</h3></td><td width="50%" valign="top"><h3>B</h3></td></tr></tbody></table>` +
		`<p style="text-align:center"><a href="https://t.me/shop" style="display:inline-block;border-radius:16px;background-color:#229ed9;color:#ffffff">Telegram</a></p>`
	if _, loc, _ := do(annaC, "POST", "/marketing/campaigns/"+promo.ID, url.Values{"title": {"Блоки"}, "box": {box}, "subject": {promo.Subject},
		"html": {blocks}}); strings.Contains(loc, "err") {
		t.Fatalf("save the blocks: %s", loc)
	}
	if c, _ := srv.Marketing.Campaign(ctx, promo.ID); !strings.Contains(c.Body, `width="50%"`) || !strings.Contains(c.Body, `valign="top"`) ||
		!strings.Contains(c.Body, `href="https://t.me/shop"`) || !strings.Contains(c.Body, "border-radius: 16px") || !strings.Contains(c.Body, "table-layout: fixed") {
		t.Fatalf("the blocks in the letter: %s", c.Body)
	}
	if code, _, _ := do(annaC, "POST", "/marketing/templates/base-nosuch/use", url.Values{}); code == http.StatusSeeOther {
		if all, _ := srv.Marketing.Campaigns(ctx); len(all) != 2 {
			t.Fatal("a campaign of an unknown template")
		}
	}
	if _, loc, _ := do(annaC, "POST", "/marketing/campaigns/"+promo.ID, url.Values{"title": {"Акция ноября"}, "box": {box}, "subject": {promo.Subject},
		"html": {promo.Body}, "go": {"template"}}); !strings.Contains(loc, "msg=") {
		t.Fatalf("save as template: %s", loc)
	}
	tpls, err := srv.Marketing.Templates(ctx)
	must(err)
	if len(tpls) != 1 || tpls[0].Name != "Акция ноября" || tpls[0].CreatedBy != anna || !strings.Contains(tpls[0].Body, "SALE20") {
		t.Fatalf("templates: %+v", tpls)
	}
	tpl := tpls[0].ID
	if code, _, body := do(borisC, "GET", "/marketing/templates?edit="+tpl, nil); code != http.StatusOK || !strings.Contains(body, "Акция ноября") ||
		strings.Contains(body, `action="/marketing/templates/`+tpl+`"`) || strings.Contains(body, `/marketing/templates/`+tpl+`/delete`) {
		t.Fatalf("Boris sees Anna's template to change: %d", code)
	}
	do(borisC, "POST", "/marketing/templates/"+tpl, url.Values{"name": {"Чужой"}, "html": {"<p>x</p>"}})
	do(borisC, "POST", "/marketing/templates/"+tpl+"/delete", nil)
	if got, err := srv.Marketing.Template(ctx, tpl); err != nil || got.Name != "Акция ноября" {
		t.Fatalf("Boris changed Anna's template: %+v %v", got, err)
	}
	if code, _, body := do(annaC, "GET", "/marketing/templates?edit="+tpl, nil); code != http.StatusOK || !strings.Contains(body, `action="/marketing/templates/`+tpl+`"`) {
		t.Fatalf("Anna's template editor: %d", code)
	}
	if _, loc, _ := do(annaC, "POST", "/marketing/templates/"+tpl, url.Values{"name": {"Акция"}, "subject": {"Скидка"}, "html": {`<p>Привет, {Name}<script>x</script></p>`}}); !strings.Contains(loc, "msg=") {
		t.Fatalf("template saved: %s", loc)
	}
	if got, _ := srv.Marketing.Template(ctx, tpl); got.Body != "<p>Привет, {Name}</p>" || got.Subject != "Скидка" {
		t.Fatalf("the template: %+v", got)
	}
	if _, loc, _ := do(annaC, "POST", "/marketing/templates/new", url.Values{"name": {""}}); !strings.Contains(loc, "edit=new") {
		t.Fatalf("a template without a name: %s", loc)
	}
	if _, loc, _ = do(borisC, "POST", "/marketing/templates/"+tpl+"/use", url.Values{}); !strings.HasPrefix(loc, "/marketing/campaigns/") {
		t.Fatalf("Boris uses Anna's template: %s", loc)
	}
	if c, _ := srv.Marketing.Campaign(ctx, strings.TrimPrefix(loc, "/marketing/campaigns/")); c.CreatedBy != boris || c.Body != "<p>Привет, {Name}</p>" || c.MailboxID != "" {
		t.Fatalf("Boris's campaign of the template: %+v", c)
	}
	if _, loc, _ := do(annaC, "POST", "/marketing/templates/"+tpl+"/delete", nil); loc != "/marketing/templates" {
		t.Fatalf("delete template: %s", loc)
	}
	if tpls, _ := srv.Marketing.Templates(ctx); len(tpls) != 0 {
		t.Fatalf("deleted template: %+v", tpls)
	}
	// REST: segments, templates, a draft of a template started, paused, stopped; the stop list; a key of another
	// user can't drive Anna's campaign
	key, err := srv.Keys.Create(ctx, anna, "mkt", []string{"crm:read", "crm:write"}, 0)
	must(err)
	bkey, err := srv.Keys.Create(ctx, boris, "mkt", []string{"crm:read", "crm:write"}, 0)
	must(err)
	api := func(k, method, path string, in any) (int, string) {
		t.Helper()
		var body io.Reader
		if in != nil {
			raw, _ := json.Marshal(in)
			body = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var flat bytes.Buffer
		if json.Compact(&flat, raw) != nil {
			return res.StatusCode, string(raw)
		}
		return res.StatusCode, flat.String()
	}
	if code, body := api(key, "GET", "/api/v1/marketing/segments?count=1", nil); code != http.StatusOK || !strings.Contains(body, `"name":"Выставка"`) || !strings.Contains(body, `"count":2`) {
		t.Fatalf("API segments: %d %s", code, body)
	}
	if code, body := api(key, "GET", "/api/v1/marketing/templates", nil); code != http.StatusOK || !strings.Contains(body, `"id":"base-promo"`) || strings.Contains(body, "<table") {
		t.Fatalf("API templates: %d %.300s", code, body)
	}
	code, body := api(key, "POST", "/api/v1/marketing/campaigns", map[string]any{"template_id": "base-news", "mailbox_id": box,
		"segments": []string{seg["Выставка"]}, "send_at": time.Now().Add(time.Hour), "start": true})
	var made struct {
		ID, Status, Subject, HTML string
		Recipients                int
	}
	_ = json.Unmarshal([]byte(body), &made)
	if code != http.StatusCreated || made.Status != "planned" || made.Recipients != 1 || made.Subject == "" || !strings.Contains(made.HTML, "{Name}") {
		t.Fatalf("API campaign: %d %.400s", code, body)
	}
	if code, _ := api(bkey, "POST", "/api/v1/marketing/campaigns/"+made.ID+"/stop", nil); code != http.StatusForbidden {
		t.Fatalf("Boris stops Anna's campaign: %d", code)
	}
	for _, a := range []string{"pause", "resume", "stop"} {
		if code, body := api(key, "POST", "/api/v1/marketing/campaigns/"+made.ID+"/"+a, nil); code != http.StatusOK {
			t.Fatalf("API %s: %d %s", a, code, body)
		}
	}
	if code, body := api(key, "GET", "/api/v1/marketing/campaigns/"+made.ID, nil); code != http.StatusOK || !strings.Contains(body, `"status":"stopped"`) {
		t.Fatalf("API campaign after stop: %d %.300s", code, body)
	}
	if code, body := api(key, "GET", "/api/v1/marketing/campaigns", nil); code != http.StatusOK || !strings.Contains(body, made.ID) || strings.Contains(body, `"html"`) {
		t.Fatalf("API campaigns: %d %.300s", code, body)
	}
	if code, _ := api(key, "POST", "/api/v1/marketing/campaigns", map[string]any{"title": "Чужой ящик", "mailbox_id": "00000000-0000-0000-0000-000000000000"}); code < 400 {
		t.Fatalf("a campaign from a mailbox not hers: %d", code)
	}
	if code, body := api(key, "POST", "/api/v1/marketing/campaigns", map[string]any{"title": "Некому", "mailbox_id": box, "subject": "x", "html": "<p>x</p>", "start": true}); code < 400 {
		t.Fatalf("started without addressees: %d %s", code, body)
	} else if all, _ := srv.Marketing.Campaigns(ctx); len(all) != 4 {
		t.Fatalf("a failed start leaves a draft: %d campaigns", len(all))
	}
	if code, body := api(key, "POST", "/api/v1/marketing/stoplist", map[string]any{"emails": []string{"Spam@X.test", "ivan@client.test"}}); code != http.StatusOK || body != `{"added":1}` {
		t.Fatalf("API stop add: %d %s", code, body)
	}
	if code, body := api(key, "GET", "/api/v1/marketing/stoplist?q=spam", nil); code != http.StatusOK || !strings.Contains(body, `"email":"spam@x.test"`) {
		t.Fatalf("API stop list: %d %s", code, body)
	}
	if code, _ := api(key, "DELETE", "/api/v1/marketing/stoplist/spam@x.test", nil); code != http.StatusNoContent {
		t.Fatalf("API stop remove: %d", code)
	}
	if code, body := api(key, "GET", "/api/v1/marketing/stoplist?q=spam", nil); code != http.StatusOK || body != "[]" {
		t.Fatalf("API stop list after remove: %s", body)
	}
	// A/B test: a share at random gets A and B, the rest wait; the test's hours over, the variant opened more goes to them
	do(annaC, "POST", "/marketing/segments/new", url.Values{"name": {"АБ"}, "entity": {"list"},
		"emails": {"ab1@ab.test\nab2@ab.test\nab3@ab.test\nab4@ab.test\nab5@ab.test\nab6@ab.test"}})
	gs, _ := srv.Marketing.Segments(ctx)
	var abSeg string
	for _, g := range gs {
		if g.Name == "АБ" {
			abSeg = g.ID
		}
	}
	code, loc, _ = do(annaC, "POST", "/marketing/campaigns/new", url.Values{"title": {"АБ"}, "box": {box}, "subject": {"Тема A"}, "html": {"<p>Письмо A</p>"},
		"seg": {abSeg}, "ab": {"1"}, "ab_subject": {"Тема B"}, "ab_html": {""}, "ab_share": {"50"}, "ab_hours": {"2"}, "ab_metric": {"opened"}, "go": {"start"}})
	abID := strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/marketing/campaigns/")
	ab, err := srv.Marketing.Campaign(ctx, abID)
	if err != nil || !ab.AB() || ab.Status != "sending" || ab.Total != 6 || ab.ABHours != 2 {
		t.Fatalf("A/B campaign started: %d %s %+v", code, loc, ab)
	}
	sentTo := map[string]string{} // address → subject
	sender := func(_ context.Context, c marketing.Campaign, r marketing.Addressee) error {
		sentTo[r.Email] = c.Subject
		return nil
	}
	if _, err := srv.Marketing.SendDue(ctx, time.Now(), sender); err != nil {
		t.Fatal(err)
	}
	var subjA, subjB []string
	for e, subj := range sentTo {
		if subj == "Тема A" {
			subjA = append(subjA, e)
		} else {
			subjB = append(subjB, e)
		}
	}
	if len(sentTo) != 3 || len(subjA) != 2 || len(subjB) != 1 || sentTo[subjB[0]] != "Тема B" {
		t.Fatalf("the test's letters: %v", sentTo)
	}
	if ab, _ = srv.Marketing.Campaign(ctx, abID); ab.Status != "sending" || ab.Pending() != 3 {
		t.Fatalf("held for the winner: %s %d", ab.Status, ab.Pending())
	}
	if code, _, _ := do(borisC, "POST", "/marketing/campaigns/"+abID+"/winner-a", nil); code == http.StatusSeeOther {
		if ab, _ = srv.Marketing.Campaign(ctx, abID); ab.ABWinner != "" {
			t.Fatal("Boris chose the winner of Anna's test")
		}
	}
	abList, _, _ := srv.Marketing.Recipients(ctx, abID, "", 10, 0)
	for _, r := range abList {
		if r.Email == subjB[0] {
			must(srv.Marketing.Opened(ctx, r.Token))
		}
	}
	if code, _, body := do(annaC, "GET", "/marketing/campaigns/"+abID, nil); code != http.StatusOK || !strings.Contains(body, "mkt-ab-res") ||
		!strings.Contains(body, "/winner-b") || !strings.Contains(body, "Тема B") {
		t.Fatalf("the test's report: %d", code)
	}
	if _, err := srv.Marketing.SendDue(ctx, time.Now().Add(time.Hour), sender); err != nil || len(sentTo) != 3 {
		t.Fatalf("decided before its hours: %v %v", err, sentTo)
	}
	if _, err := srv.Marketing.SendDue(ctx, time.Now().Add(3*time.Hour), sender); err != nil {
		t.Fatal(err)
	}
	if ab, _ = srv.Marketing.Campaign(ctx, abID); ab.ABWinner != "b" || len(sentTo) != 6 || ab.Status != "done" {
		t.Fatalf("the winner B to the rest: %q %s %v", ab.ABWinner, ab.Status, sentTo)
	}
	nb := 0
	for _, subj := range sentTo {
		if subj == "Тема B" {
			nb++
		}
	}
	if res, _ := srv.Marketing.ABResults(ctx, abID); nb != 4 || res["b"].Opened != 1 || res["a"].Sent != 2 {
		t.Fatalf("the test's results: %d %+v", nb, res)
	}
	if err := srv.Marketing.ChooseWinner(ctx, anna, false, abID, "a"); err == nil {
		t.Fatal("a winner chosen twice")
	}
	if code, body := api(key, "GET", "/api/v1/marketing/campaigns/"+abID, nil); code != http.StatusOK || !strings.Contains(body, `"winner":"b"`) {
		t.Fatalf("API A/B: %s", body)
	}
	// «Обзвон»: the segment's cards with a phone get a «Звонок» deed of the chosen employee; the progress is the deeds done
	_, err = pool.Exec(ctx, `UPDATE crm_contacts SET phone = '+7 900 000-00-0' || length(name) WHERE name IN ('Иван', 'Пётр')`)
	must(err)
	deadline := time.Now().Add(24 * time.Hour).Format("2006-01-02T15:04")
	_, loc, _ = do(annaC, "POST", "/marketing/calls/new", url.Values{"title": {"Список"}, "segment": {seg["Выставка"]}, "deadline": {deadline}, "go": {"start"}})
	if cl, err := srv.Marketing.CallList(ctx, strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/marketing/calls/")); err != nil || cl.Status != "draft" {
		t.Fatalf("a call list of addresses started: %s %+v", loc, cl)
	}
	code, loc, _ = do(annaC, "POST", "/marketing/calls/new", url.Values{"title": {"Приглашение"}, "segment": {seg["Все контакты"]}, "script": {"Пригласить на выставку"},
		"assign": {"staff"}, "staff": {boris}, "deadline": {deadline}, "go": {"start"}})
	callID := strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/marketing/calls/")
	cl, err := srv.Marketing.CallList(ctx, callID)
	if err != nil || cl.Status != "started" || cl.Total != 2 || cl.Done != 0 {
		t.Fatalf("call list started: %d %s %+v", code, loc, cl)
	}
	var calls, notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_activities WHERE kind = 'call' AND responsible_id = $1 AND subject = 'Приглашение'
		AND description = 'Пригласить на выставку'`, boris).Scan(&calls))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND body = 'Приглашение (2)'`, boris).Scan(&notes))
	if calls != 2 || notes != 1 {
		t.Fatalf("the calls' deeds: %d, notifications: %d", calls, notes)
	}
	_, err = pool.Exec(ctx, `UPDATE crm_activities SET done_at = now() WHERE id = (SELECT activity_id FROM mkt_call_items WHERE call_id = $1 ORDER BY id LIMIT 1)`, callID)
	must(err)
	if cl, _ = srv.Marketing.CallList(ctx, callID); cl.Done != 1 {
		t.Fatalf("a call made: %+v", cl)
	}
	if code, _, body := do(annaC, "GET", "/marketing/calls/"+callID+"?done=0", nil); code != http.StatusOK || strings.Count(body, `href="tel:`) != 1 {
		t.Fatalf("the calls left: %d", code)
	}
	if code, _, body := do(annaC, "GET", "/marketing/calls", nil); code != http.StatusOK || !strings.Contains(body, "Приглашение") {
		t.Fatalf("call lists: %d", code)
	}
	if code, body := api(key, "GET", "/api/v1/marketing/calls/"+callID+"?done=1", nil); code != http.StatusOK || strings.Count(body, `"done_at"`) != 1 ||
		!strings.Contains(body, `"cards":2,"done":1`) {
		t.Fatalf("API call list: %d %.300s", code, body)
	}
	if code, body := api(key, "POST", "/api/v1/marketing/calls", map[string]any{"title": "Некому", "segment_id": seg["Выставка"], "start": true}); code < 400 {
		t.Fatalf("API call list of addresses: %d %s", code, body)
	}
	do(borisC, "POST", "/marketing/calls/"+callID+"/delete", nil)
	if _, err := srv.Marketing.CallList(ctx, callID); err != nil {
		t.Fatal("Boris deleted Anna's call list")
	}
	do(annaC, "POST", "/marketing/calls/"+callID+"/delete", nil)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_activities WHERE kind = 'call' AND subject = 'Приглашение'`).Scan(&calls))
	if _, err := srv.Marketing.CallList(ctx, callID); err == nil || calls != 2 {
		t.Fatalf("the call list deleted, the deeds kept: %v %d", err, calls)
	}
}
