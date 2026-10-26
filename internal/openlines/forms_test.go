package openlines

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestForms: an operator sends a CRM form in the dialogue — it is the operator's answer with the link; the client
// fills it: «Форма заполнена» for both, the service line of the cards, the dialogue's lead gets the answers instead
// of a second lead, a deal form's contact is bound to the dialogue; a wrong key or form is not the dialogue's.
func TestForms(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	boss, a := newUser("boss"), newUser("a")
	c := crm.NewStore(pool)
	s := NewStore(pool, c)
	s.Tr = func(key string, args ...any) string { return key }
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Линия", Active: true, Operators: []string{a}, CRMAuto: true})
	must(err)
	sid, tok, _ := s.Visit(ctx, wid, "", "")
	must(s.VisitorSay(ctx, wid, tok, "Хочу заказать"))
	fields := []crm.FormField{{Key: "name"}, {Key: "phone"}, {Key: "comment"}}
	fid, err := c.SaveForm(ctx, boss, crm.Form{Name: "Заявка", Scheme: "lead", Active: true, Fields: fields})
	must(err)
	off, err := c.SaveForm(ctx, boss, crm.Form{Name: "Старая", Scheme: "lead", Fields: fields})
	must(err)
	link := func(token, secret string) string { return "https://go24/cf/" + token + "?ol=" + secret }

	if err := s.SendForm(ctx, a, sid, off, link); err == nil {
		t.Fatal("a form switched off was sent")
	}
	must(s.SendForm(ctx, a, sid, fid, link))
	x, msgs, err := s.Open(ctx, a, sid)
	must(err)
	m := msgs[len(msgs)-1]
	if m.Kind != "form" || m.AuthorID != a || m.FormID != fid || m.FormSecret == "" || m.FormToken == "" || m.Auto() || m.Internal() ||
		!strings.HasSuffix(m.Body, "?ol="+m.FormSecret) || m.FormDone != nil {
		t.Fatalf("sent form: %+v", m)
	}
	if x.OperatorID != a || x.Waiting {
		t.Fatalf("the form is not the operator's answer: operator %q, waiting %v", x.OperatorID, x.Waiting)
	}
	vis, err := s.VisitorMessages(ctx, wid, tok, 0)
	must(err)
	if v := vis[len(vis)-1]; v.FormSecret != m.FormSecret || v.Operator != "a" {
		t.Fatalf("the visitor's form message: %+v", v)
	}
	if x.LeadID == "" {
		t.Fatal("the line made no lead")
	}

	// a wrong key or another form's link is not the dialogue's
	if _, err := s.FormFor(ctx, fid, "nope"); err != ErrNotFound {
		t.Fatalf("a wrong key: %v", err)
	}
	if _, err := s.FormFor(ctx, off, m.FormSecret); err != ErrNotFound {
		t.Fatalf("another form with the key: %v", err)
	}
	l, err := s.FormFor(ctx, fid, m.FormSecret)
	must(err)
	if l.Session != sid || l.LeadID != x.LeadID {
		t.Fatalf("form link: %+v", l)
	}

	// filled: the dialogue's lead gets the answers, no second lead
	f, err := c.Form(ctx, fid)
	must(err)
	must(c.FormFields(ctx, &f))
	ans, err := f.Answer(func(k string) string {
		return map[string]string{"name": "Ольга", "phone": "+7 900 111-22-33", "comment": "Два ящика"}[k]
	}, false)
	must(err)
	res, err := c.SubmitForm(ctx, f, ans, crm.FormFilling{Title: "Заявка — Ольга", Session: l.Session, LeadID: l.LeadID})
	must(err)
	must(s.FormFilled(ctx, m.FormSecret, res))
	if res.LeadID != x.LeadID {
		t.Fatalf("a second lead: %q, the dialogue's %q", res.LeadID, x.LeadID)
	}
	var leads int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_leads`).Scan(&leads))
	lead, err := c.Lead(ctx, x.LeadID)
	must(err)
	if leads != 1 || lead.Name != "Ольга" || lead.Phone == "" || strings.Count(lead.Comment, "Два ящика") != 1 {
		t.Fatalf("the dialogue's lead: %d leads, %+v", leads, lead)
	}
	x, msgs, err = s.Open(ctx, a, sid)
	must(err)
	if x.Name != "Ольга" || x.Phone != "+7 900 111-22-33" {
		t.Fatalf("the dialogue's client: %q %q", x.Name, x.Phone)
	}
	var sent, done, note bool
	for _, m := range msgs {
		switch {
		case m.Kind == "form":
			sent = m.FormDone != nil
		case m.Kind == "form_done":
			done = m.Auto() && m.FormID == fid && m.Body == "ol.form_done"
		case m.Kind == "note" && m.Body == "ol.note_form":
			note = true
		}
	}
	if !sent || !done || !note {
		t.Fatalf("after filling: sent marked %v, «filled» %v, service line %v", sent, done, note)
	}
	var session string
	must(pool.QueryRow(ctx, `SELECT coalesce(ol_session_id::text, '') FROM crm_form_results WHERE id = $1`, res.ID).Scan(&session))
	if session != sid {
		t.Fatalf("result's dialogue: %q", session)
	}
	if rs, err := c.FormResults(ctx, fid, 10); err != nil || len(rs) != 1 || rs[0].Session != sid {
		t.Fatalf("form results: %+v, %v", rs, err)
	}

	// a deal form in a dialogue without a lead: the contact is bound to it
	pipes, err := c.Pipelines(ctx)
	must(err)
	dfid, err := c.SaveForm(ctx, boss, crm.Form{Name: "Сделка", Scheme: "deal", Active: true, PipelineID: pipes[0].ID, Fields: fields})
	must(err)
	sid2, tok2, _ := s.Visit(ctx, wid, "", "")
	must(s.VisitorSay(ctx, wid, tok2, "Здравствуйте"))
	_, err = pool.Exec(ctx, `UPDATE ol_sessions SET lead_id = NULL WHERE id = $1`, sid2)
	must(err)
	must(s.SendForm(ctx, a, sid2, dfid, link))
	_, msgs, err = s.Open(ctx, a, sid2)
	must(err)
	key := msgs[len(msgs)-1].FormSecret
	l, err = s.FormFor(ctx, dfid, key)
	must(err)
	df, err := c.Form(ctx, dfid)
	must(err)
	must(c.FormFields(ctx, &df))
	ans, err = df.Answer(func(k string) string { return map[string]string{"name": "Пётр", "phone": "+7 900 555-00-00"}[k] }, false)
	must(err)
	res, err = c.SubmitForm(ctx, df, ans, crm.FormFilling{Title: "Сделка — Пётр", Session: l.Session, LeadID: l.LeadID})
	must(err)
	must(s.FormFilled(ctx, key, res))
	x, _, err = s.Open(ctx, a, sid2)
	must(err)
	if res.DealID == "" || x.ContactID != res.ContactID || x.LeadID != "" {
		t.Fatalf("deal form: deal %q, dialogue's contact %q (made %q), lead %q", res.DealID, x.ContactID, res.ContactID, x.LeadID)
	}
	if err := s.FormFilled(ctx, "nope", res); err != ErrNotFound {
		t.Fatalf("filled with a wrong key: %v", err)
	}
}
