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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/marketing"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

func TestPhoneText(t *testing.T) {
	for in, want := range map[string]string{"+74951234567": "+7 495 123-45-67", "74951234567": "+7 495 123-45-67",
		"9001234567": "+7 900 123-45-67", "88001234567": "88001234567", "+4930123456": "+4930123456"} {
		if got := phoneText(in); got != want {
			t.Errorf("phoneText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCallTracking: «Колл-трекинг» — a line is the number of an advertising channel; /ct.js gives a site the
// company's numbers and the channels' lines; a call from an unknown number to the line makes a lead with the channel's
// «Источник» and utm_source, a call to a line of no channel — the usual one.
func TestCallTracking(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	store := telephony.NewStore(pool, "secret")
	must(store.SetNumber(ctx, anna, "101"))
	must(store.SetEnabled(ctx, anna, true))
	must(store.SetLeadAuto(ctx, anna, true))
	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	cs := crm.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), I18n: bundle, pages: pages, CRM: cs, Marketing: marketing.NewStore(pool, cs),
		Telephony: &telephony.PBX{Store: store, Key: "astkey", Records: t.TempDir(), ARI: &telephony.ARI{URL: "http://127.0.0.1:1", User: "go24", Password: "x"}},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	get := func(path string) (int, string) {
		t.Helper()
		res, err := http.Get(hs.URL + path)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	// no lines of channels yet: the script does nothing
	if st, js := get("/ct.js"); st != http.StatusOK || !strings.Contains(js, `const cfg = {"lines":[],"numbers":[]}`) {
		t.Fatalf("the script without lines: %d\n%.300s", st, js)
	}
	adv, err := cs.DictCode(ctx, "source", "ADVERTISING")
	must(err)
	yandex, err := srv.Marketing.SaveAdSource(ctx, marketing.AdSource{Name: "Яндекс Директ", UTM: []string{"Yandex", "ya-direct"}, CRMSource: adv})
	must(err)
	main, err := store.SaveTrunk(ctx, anna, telephony.Trunk{Name: "Основной", Host: "sip.example.ru", Username: "u1", Password: "p", Number: "+74951234567", OutAll: true})
	must(err)
	direct, err := store.SaveTrunk(ctx, anna, telephony.Trunk{Name: "Директ", Host: "sip.example.ru", Username: "u2", Password: "p", Number: "+7 (495) 765-43-21", OutAll: true})
	must(err)
	must(store.SetTrunkAdSource(ctx, direct, yandex))
	must(store.SetTrunkAdSource(ctx, main, "00000000-0000-0000-0000-000000000000")) // no such channel — none
	trunks, err := store.Trunks(ctx)
	must(err)
	if len(trunks) != 2 || trunks[0].AdSource != "" || trunks[1].AdSource != yandex {
		t.Fatalf("the lines' channels: %+v", trunks)
	}
	if err := store.SetTrunkAdSource(ctx, "00000000-0000-0000-0000-000000000000", yandex); err != telephony.ErrNotFound {
		t.Fatalf("the channel of no line: %v", err)
	}
	st, js := get("/ct.js")
	if want := `const cfg = {"lines":[{"number":"+74957654321","text":"+7 495 765-43-21","channels":["yandex","ya-direct"]}],"numbers":["+74951234567","+74957654321"]}`; st != http.StatusOK || !strings.Contains(js, want) {
		t.Fatalf("the script: %d\n%.400s", st, js)
	}

	// the settings show the channel of the line and the site's code
	sess := func() *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: anna, Name: "Анна", Admin: true}))
		return rec.Result().Cookies()[0]
	}()
	req, _ := http.NewRequest("GET", hs.URL+"/settings/telephony", nil)
	req.AddCookie(sess)
	res, err := http.DefaultClient.Do(req)
	must(err)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if p := string(page); res.StatusCode != http.StatusOK || !strings.Contains(p, `id="tracking"`) || !strings.Contains(p, "/ct.js") ||
		!strings.Contains(p, `<option value="`+yandex+`" selected>Яндекс Директ</option>`) {
		t.Fatalf("the settings: %d\n%s", res.StatusCode, p)
	}

	// a call to the channel's line: the lead is the channel's; to the other line — the usual one
	in := func(id, from, line string) {
		t.Helper()
		res, err := http.PostForm(hs.URL+"/telephony/cdr", url.Values{"key": {"astkey"}, "id": {id}, "from": {from}, "to": {"101"},
			"line": {line}, "start": {"1761401000"}, "end": {"1761401090"}, "answer": {"1761401010"}, "dial": {"ANSWER"}})
		must(err)
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("cdr %s: %d", id, res.StatusCode)
		}
	}
	lead := func(number string) crm.Lead {
		t.Helper()
		d, err := cs.Duplicates(ctx, "lead", "", "", number, "")
		must(err)
		if len(d) != 1 {
			t.Fatalf("the leads of %s: %+v", number, d)
		}
		l, err := cs.Lead(ctx, d[0].ID)
		must(err)
		return l
	}
	in("t.1", "+79001110001", "+74957654321")
	l := lead("+79001110001")
	if l.Origin.ID != adv || l.Source != "Колл-трекинг: Яндекс Директ" {
		t.Fatalf("the channel's lead: %+v %q", l.Origin, l.Source)
	}
	if u, _, err := srv.Marketing.UTMOf(ctx, "lead", l.ID); err != nil || u.Source != "Yandex" || u.Medium != "calltracking" {
		t.Fatalf("the lead's UTM: %+v %v", u, err)
	}
	in("t.2", "+79001110002", "+74951234567")
	l = lead("+79001110002")
	if l.Origin.Code != "CALL" || l.Source != "Телефония go24" {
		t.Fatalf("the usual lead: %+v %q", l.Origin, l.Source)
	}
	if u, _, err := srv.Marketing.UTMOf(ctx, "lead", l.ID); err != nil || !u.IsZero() {
		t.Fatalf("the usual lead's UTM: %+v %v", u, err)
	}

	// «Создавать в CRM» of a line: the channel's line makes a contact and a deal for every call, the main one nothing
	// for incoming calls and a lead for outgoing ones
	must(store.SetTrunkCRM(ctx, direct, "deal", "all"))
	must(store.SetTrunkCRM(ctx, main, "lead", "out"))
	if err := store.SetTrunkCRM(ctx, "00000000-0000-0000-0000-000000000000", "lead", "in"); err != telephony.ErrNotFound {
		t.Fatalf("«Создавать в CRM» of no line: %v", err)
	}
	out := func(id, to, line string) {
		t.Helper()
		res, err := http.PostForm(hs.URL+"/telephony/cdr", url.Values{"key": {"astkey"}, "id": {id}, "from": {"101"}, "to": {to}, "by": {"101"},
			"line": {line}, "start": {"1761401000"}, "end": {"1761401090"}, "answer": {"1761401010"}, "dial": {"ANSWER"}})
		must(err)
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("cdr %s: %d", id, res.StatusCode)
		}
	}
	client := func(number string) map[string]string {
		t.Helper()
		d, err := cs.Duplicates(ctx, "lead", "", "", number, "")
		must(err)
		got := map[string]string{}
		for _, x := range d {
			got[x.Entity] = x.ID
		}
		return got
	}
	deal := func(number string) crm.Deal {
		t.Helper()
		var id string
		must(pool.QueryRow(ctx, `SELECT d.id::text FROM crm_deals d WHERE d.contact_id::text = $1`, client(number)["contact"]).Scan(&id))
		d, err := cs.Deal(ctx, id)
		must(err)
		return d
	}
	in("t.3", "+79001110003", "+74957654321")
	if c := client("+79001110003"); c["contact"] == "" || c["lead"] != "" {
		t.Fatalf("an incoming call of the deal's line: %v", c)
	}
	if d := deal("+79001110003"); d.Origin.ID != adv || d.Responsible.ID != anna || d.Title != "Звонок: +79001110003" {
		t.Fatalf("the deal: %+v", d)
	} else if u, _, err := srv.Marketing.UTMOf(ctx, "deal", d.ID); err != nil || u.Source != "Yandex" {
		t.Fatalf("the deal's UTM: %+v %v", u, err)
	}
	var bound string
	must(pool.QueryRow(ctx, `SELECT crm_entity FROM telephony_calls WHERE uniqueid = 't.3'`).Scan(&bound))
	if bound != "deal" {
		t.Fatalf("the call's card: %q", bound)
	}
	out("t.4", "+79001110004", "+74957654321")
	if d := deal("+79001110004"); d.Origin.ID != adv {
		t.Fatalf("an outgoing call's deal: %+v", d)
	}
	in("t.5", "+79001110005", "+74951234567")
	if c := client("+79001110005"); len(c) != 0 {
		t.Fatalf("an incoming call of the line making leads of outgoing ones: %v", c)
	}
	out("t.6", "+79001110006", "+74951234567")
	if l := lead("+79001110006"); l.Origin.Code != "" || l.Responsible.ID != anna {
		t.Fatalf("an outgoing call's lead: %+v", l)
	}
	must(store.SetTrunkCRM(ctx, main, "none", "all"))
	out("t.7", "+79001110007", "+74951234567")
	if c := client("+79001110007"); len(c) != 0 {
		t.Fatalf("a line making nothing: %v", c)
	}
}
