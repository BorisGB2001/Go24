package crm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

func TestRobotCondOK(t *testing.T) {
	v := map[string]string{"amount": "1500", "title": "Окна для ООО Ромашка", "comment": ""}
	for _, c := range []struct {
		cond RobotCond
		want bool
	}{
		{RobotCond{"amount", "gt", "1 000"}, true},
		{RobotCond{"amount", "lt", "1000"}, false},
		{RobotCond{"amount", "eq", "1500,00"}, true},
		{RobotCond{"amount", "ne", "1500"}, false},
		{RobotCond{"title", "contains", "ромашка"}, true},
		{RobotCond{"title", "eq", "окна для ооо ромашка"}, true},
		{RobotCond{"title", "gt", "1"}, false},
		{RobotCond{"comment", "empty", ""}, true},
		{RobotCond{"comment", "filled", ""}, false},
		{RobotCond{"uf:x", "empty", ""}, true},
	} {
		if got := robotCondOK(c.cond, v); got != c.want {
			t.Errorf("%+v = %v", c.cond, got)
		}
	}
}

// TestRobotFields: a fields robot sets built-in and own fields of a deal and a lead (with the history), a condition
// lets a robot run only for the cards that meet it, a webhook robot posts the card and writes the answer into the
// history.
func TestRobotFields(t *testing.T) {
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	s := NewStore(pool)
	ts := tasks.NewStore(pool)
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Этап", Kind: "list", Options: []string{"КП", "Договор"}}))
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "VIP", Kind: "bool"}))
	fields, err := ts.FieldsOf(ctx, tasks.CRMFieldSets["deal"])
	must(err)
	var phase, vip string
	for _, f := range fields {
		if f.Name == "VIP" {
			vip = "uf:" + f.ID
		} else {
			phase = "uf:" + f.ID
		}
	}
	dicts, err := s.Dicts(ctx)
	must(err)
	web := dicts["source"][0].ID

	pipes, _ := s.Pipelines(ctx)
	st := pipes[0].Stages
	var ve *i18n.Error
	for _, bad := range []struct {
		kind string
		p    RobotParams
	}{
		{"fields", RobotParams{}},
		{"fields", RobotParams{Set: []RobotSet{{Field: "responsible", Value: boss}}}},
		{"fields", RobotParams{Set: []RobotSet{{Field: "phone", Value: "1"}}}}, // a lead's, not a deal's
		{"fields", RobotParams{Set: []RobotSet{{Field: "uf:00000000-0000-0000-0000-000000000000", Value: "1"}}}},
		{"webhook", RobotParams{URL: "ftp://x"}},
		{"notify", RobotParams{Text: "x", If: &RobotCond{Field: "title", Op: "like"}}},
		{"notify", RobotParams{Text: "x", If: &RobotCond{Field: "nope", Op: "eq"}}},
	} {
		if err := s.AddRobot(ctx, boss, st[1].ID, bad.kind, bad.p); !errors.As(err, &ve) {
			t.Fatalf("%s %+v: %v", bad.kind, bad.p, err)
		}
	}
	must(s.AddRobot(ctx, boss, st[1].ID, "fields", RobotParams{Set: []RobotSet{{Field: "title", Value: "{title} — в работе"},
		{Field: "origin", Value: web}, {Field: phase, Value: "Договор"}, {Field: vip, Value: "1"}, {Field: "comment"}},
		If: &RobotCond{Field: "amount", Op: "gt", Value: "1000"}}))
	must(s.AddRobot(ctx, boss, st[1].ID, "comment", RobotParams{Text: "VIP", If: &RobotCond{Field: vip, Op: "filled", Value: "ignored"}}))

	small, err := s.SaveDeal(ctx, boss, Deal{Title: "Малая", PipelineID: pipes[0].ID, Amount: 500, Comment: "c"})
	must(err)
	big, err := s.SaveDeal(ctx, boss, Deal{Title: "Крупная", PipelineID: pipes[0].ID, Amount: 5000, Comment: "c"})
	must(err)
	for _, id := range []string{small, big} {
		_, err = s.MoveDeal(ctx, boss, id, st[1].ID)
		must(err)
	}
	d, _ := s.Deal(ctx, small)
	if d.Title != "Малая" || d.Comment != "c" {
		t.Fatalf("the condition did not hold the robot: %+v", d)
	}
	d, _ = s.Deal(ctx, big)
	if d.Title != "Крупная — в работе" || d.Origin.ID != web || d.Comment != "" || d.Amount != 5000 {
		t.Fatalf("set deal = %+v", d)
	}
	vals, err := s.cardValues(ctx, dealCard(d))
	must(err)
	if vals[phase] != "Договор" || vals[vip] != "1" {
		t.Fatalf("own fields = %v", vals)
	}
	var changed, vipNote int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id = $1 AND kind = 'changed'`, big).Scan(&changed))
	// the comment robot runs after the fields robot set VIP: robots see the card as it is when they run
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id = $1 AND kind = 'robot' AND body = 'VIP'`, big).Scan(&vipNote))
	if changed < 2 || vipNote != 1 {
		t.Fatalf("history: changed %d, VIP note %d", changed, vipNote)
	}

	// a lead: its own built-in fields; a bad value is the robot's failure in the history
	lst, err := s.LeadStages(ctx)
	must(err)
	must(s.AddRobot(ctx, boss, lst[1].ID, "fields", RobotParams{Set: []RobotSet{{Field: "phone", Value: "+7 900 000-00-00"},
		{Field: "source_info", Value: "робот {responsible}"}}}))
	must(s.AddRobot(ctx, boss, lst[1].ID, "fields", RobotParams{Set: []RobotSet{{Field: "amount", Value: "много"}}}))
	lead, err := s.SaveLead(ctx, boss, Lead{Title: "Заявка"})
	must(err)
	_, err = s.MoveLead(ctx, boss, lead, lst[1].ID)
	must(err)
	l, _ := s.Lead(ctx, lead)
	if l.Phone != "+7 900 000-00-00" || l.Source != "робот boss" {
		t.Fatalf("set lead = %+v", l)
	}
	var failed int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id = $1 AND body = 'fail:fields:err.crm_amount'`, lead).Scan(&failed))
	if failed != 1 {
		t.Fatalf("a failed fields robot is not in the history")
	}

	// webhook: the card goes to the address; a failing address is written as the robot's failure
	got := make(chan RobotHook, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var h RobotHook
		_ = json.Unmarshal(raw, &h)
		if strings.HasSuffix(r.URL.Path, "/bad") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		got <- h
	}))
	defer srv.Close()
	must(s.AddRobot(ctx, boss, st[2].ID, "webhook", RobotParams{URL: srv.URL + "/hook"}))
	must(s.AddRobot(ctx, boss, st[2].ID, "webhook", RobotParams{URL: srv.URL + "/bad"}))
	_, err = s.MoveDeal(ctx, boss, big, st[2].ID)
	must(err)
	select {
	case h := <-got:
		if h.Event != "crm.robot" || h.Entity != "deal" || h.ID != big || h.Amount != 5000 || h.Stage != st[2].ID || h.Fields[phase] != "Договор" {
			t.Fatalf("hook = %+v", h)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no webhook")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var ok, bad int
		must(pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE body LIKE 'webhook:%'), count(*) FILTER (WHERE body = 'fail:webhook:HTTP 500')
			FROM crm_timeline WHERE entity_id = $1 AND kind = 'robot'`, big).Scan(&ok, &bad))
		if ok == 1 && bad == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook history: ok %d, failed %d", ok, bad)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
