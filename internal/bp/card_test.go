package bp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestCardSteps: a deal's process changes its card (built-in and own fields, the responsible), creates a contact of
// the deal's company, writes to people in the messenger and a letter to the client; a process changing its own card
// does not start itself again.
func TestCardSteps(t *testing.T) {
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
	boss, ann := newUser("boss"), newUser("Анна Петрова")
	ts := tasks.NewStore(pool)
	cs := crm.NewStore(pool)
	cs.Tasks = ts
	type letter struct{ from, to, subject, text string }
	var letters []letter
	cs.SendMail = func(_ context.Context, userID, to, subject, text string) error {
		letters = append(letters, letter{userID, to, subject, text})
		return nil
	}
	s := NewStore(pool, ts, cs, company.NewStore(pool))
	type message struct{ from, to, text string }
	var messages []message
	s.Chat = func(_ context.Context, from, to, text string) error {
		messages = append(messages, message{from, to, text})
		return nil
	}
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Регион", Kind: "string"}))
	firm, err := cs.SaveCompany(ctx, boss, crm.Company{Title: "Ромашка", Email: "info@romashka.test"})
	must(err)
	pipes, err := cs.Pipelines(ctx)
	must(err)
	deal, err := cs.SaveDeal(ctx, boss, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Company: crm.Ref{ID: firm}})
	must(err)

	var ve *i18n.Error
	bad := []Step{
		{Kind: "update"},                      // nothing to set
		{Kind: "create", Text: "X"},           // no kind of card
		{Kind: "mail", To: "client"},          // no subject
		{Kind: "chat", Text: "x", Email: "a"}, // an e-mail of a chat
		{Kind: "update", Sets: []Assign{{Field: "Нет такого", Value: "1"}}},
	}
	for i, st := range bad {
		if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Entity: "deal", Steps: []Step{st}}); !errors.As(err, &ve) {
			t.Fatalf("bad step %d saved: %v", i, err)
		}
	}
	if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Без карточки", Steps: []Step{{Kind: "mail", To: "client", Subject: "S"}}}); !errors.As(err, &ve) {
		t.Fatalf("a letter to the client of no card: %v", err)
	}

	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Обработка сделки", Entity: "deal", Active: true, AutoChange: true,
		Fields: []Field{{Key: "sum", Label: "Сумма", Kind: "number"}, {Key: "person", Label: "Контакт", Kind: "string"}},
		Steps: []Step{
			{Kind: "update", Sets: []Assign{{Field: "amount", Value: "{sum}"}, {Field: "регион", Value: "Север"},
				{Field: "responsible", Value: "Анна Петрова"}, {Field: "comment", Value: "Сумма от {starter}"}}},
			{Kind: "create", Entity: "contact", Text: "{person}", To: "responsible", Sets: []Assign{{Field: "phone", Value: "+7 900"}}},
			{Kind: "chat", From: "starter", To: "responsible", Users: []string{boss}, Text: "Сделка «{title}» на {sum}: {created_card}"},
			{Kind: "mail", From: "responsible", To: "client", Email: "buh@romashka.test", Subject: "Счёт по «{title}»", Text: "Сумма {sum}"},
		}})
	must(err)
	id, err := s.Start(ctx, boss, tpl, deal, map[string]string{"sum": "1500,5", "person": "Иван Сидоров"})
	must(err)
	x, err := s.instance(ctx, id)
	must(err)
	if x.Status != "done" {
		lines, _ := s.Log(ctx, id)
		t.Fatalf("status %s: %+v", x.Status, lines)
	}
	d, err := cs.Deal(ctx, deal)
	must(err)
	if d.Amount != 1500.5 || d.Responsible.ID != ann || d.Comment != "Сумма от boss" {
		t.Fatalf("the deal: %+v", d)
	}
	vals, err := ts.FieldValuesOf(ctx, tasks.CRMFieldSets["deal"], deal)
	must(err)
	if len(vals) != 1 || vals[0].Value != "Север" {
		t.Fatalf("own field: %+v", vals)
	}
	path, ok := strings.CutPrefix(x.Values["created_card"], "/crm/contacts/")
	if !ok {
		t.Fatalf("created card: %q", x.Values["created_card"])
	}
	c, err := cs.Contact(ctx, path)
	must(err)
	if c.Name != "Иван Сидоров" || c.Phone != "+7 900" || c.Company.ID != firm || c.Responsible.ID != ann {
		t.Fatalf("the contact: %+v", c)
	}
	// the responsible changed before: the chat is from boss to Anna; to himself it is a notification
	if len(messages) != 1 || messages[0].from != boss || messages[0].to != ann || !strings.Contains(messages[0].text, "«Поставка» на 1500,5: /crm/contacts/") {
		t.Fatalf("messages: %+v", messages)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND body LIKE 'Сделка%'`, boss).Scan(&notes))
	if notes != 1 {
		t.Fatalf("notification to oneself: %d", notes)
	}
	if len(letters) != 1 || letters[0].from != ann || letters[0].to != "info@romashka.test, buh@romashka.test" ||
		letters[0].subject != "Счёт по «Поставка»" || letters[0].text != "Сумма 1500,5" {
		t.Fatalf("letters: %+v", letters)
	}

	// the process changed its card and is done: the worker reading those events does not start it again
	autoLag = 0
	must(errors.Join(s.AutoStart(ctx)...))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM bp_instances WHERE template_id::text = $1`, tpl).Scan(&n))
	if n != 1 {
		t.Fatalf("restarted by its own changes: %d processes", n)
	}
	// a person's change after it does
	d.Title = "Поставка 2"
	_, err = cs.SaveDeal(ctx, boss, d)
	must(err)
	must(errors.Join(s.AutoStart(ctx)...))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM bp_instances WHERE template_id::text = $1`, tpl).Scan(&n))
	if n != 2 {
		t.Fatalf("a later change: %d processes", n)
	}

	// no messenger, no mailbox: the step fails the process
	s.Chat = nil
	chat, err := s.SaveTemplate(ctx, boss, Template{Name: "Чат", Active: true, Steps: []Step{{Kind: "chat", To: ann, Text: "Привет"}}})
	must(err)
	id, err = s.Start(ctx, boss, chat, "", nil)
	must(err)
	if x, _ := s.instance(ctx, id); x.Status != "failed" {
		t.Fatalf("a chat with no messenger: %s", x.Status)
	}
}
