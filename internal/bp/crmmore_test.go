package bp

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestCRMMoreSteps: a deal's process copies it into another pipeline (the copy's title and responsible set), moves the
// deal there too, changes its observers and completes its calls — the starter without CRM rights.
func TestCRMMoreSteps(t *testing.T) {
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
	boss, ann, bob := newUser("boss"), newUser("ann"), newUser("bob")
	ts := tasks.NewStore(pool)
	cs := crm.NewStore(pool)
	cs.Tasks = ts
	s := NewStore(pool, ts, cs, company.NewStore(pool))

	pipe, err := cs.AddPipeline(ctx, "Повторные")
	must(err)
	pipes, err := cs.Pipelines(ctx)
	must(err)
	var other string
	for _, p := range pipes {
		if p.ID == pipe {
			other = p.Stages[1].ID
		}
	}

	var ve *i18n.Error
	bad := []Template{
		{Name: "Плохой", Steps: []Step{{Kind: "crmmove", Stage: other}}},                                           // no deal of the process
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmcopy"}}},                                         // no stage
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmobservers", Mode: "add"}}},                       // no one to add
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmobservers", Mode: "all", Users: []string{ann}}}}, // the mode
		{Name: "Плохой", Entity: "contact", Steps: []Step{{Kind: "crmobservers", Mode: "set"}}},                    // a contact has none
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmclose", Deed: "fax"}}},                           // the kind of deeds
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmmove", Stage: other, Sets: []Assign{{Field: "title", Value: "x"}}}}},
	}
	for i, tp := range bad {
		if _, err := s.SaveTemplate(ctx, boss, tp); !errors.As(err, &ve) {
			t.Fatalf("bad template %d saved: %v", i, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Повторная продажа", Entity: "deal", Active: true,
		Steps: []Step{
			{Kind: "crmcopy", Stage: other, Sets: []Assign{{Field: "title", Value: "Повтор: {title}"}, {Field: "responsible", Value: bob}}},
			{Kind: "crmmove", Stage: other},
			{Kind: "crmobservers", Mode: "add", Users: []string{ann, "starter"}},
			{Kind: "crmobservers", Mode: "remove", Users: []string{boss}},
			{Kind: "crmclose", Deed: "call"},
			{Kind: "crmobservers", Value: "{created_card}", Mode: "set", Users: []string{bob}},
		}})
	must(err)
	deal, err := cs.SaveDeal(ctx, boss, crm.Deal{Title: "Окна", Amount: 500, PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: boss}})
	must(err)
	must(cs.SetObservers(ctx, boss, "deal", deal, []string{boss}))
	for _, kind := range []string{"call", "call", "meeting"} {
		_, err := cs.AddActivity(ctx, boss, crm.Activity{Entity: "deal", EntityID: deal, Kind: kind, Subject: kind,
			Deadline: time.Now().Add(time.Hour), Responsible: crm.Ref{ID: boss}})
		must(err)
	}

	id, err := s.Start(ctx, ann, tpl, deal, nil)
	must(err)
	x, err := s.instance(ctx, id)
	must(err)
	if x.Status != "done" {
		lines, _ := s.Log(ctx, id)
		t.Fatalf("status %s: %+v", x.Status, lines)
	}
	copied, ok := strings.CutPrefix(x.Values["created_card"], "/crm/deals/")
	if !ok || copied == deal {
		t.Fatalf("values: %+v", x.Values)
	}
	c, err := cs.Deal(ctx, copied)
	must(err)
	if c.Title != "Повтор: Окна" || c.Responsible.ID != bob || c.PipelineID != pipe || c.Stage.ID != other || c.Amount != 500 {
		t.Fatalf("the copy: %+v", c)
	}
	d, err := cs.Deal(ctx, deal)
	must(err)
	if d.PipelineID != pipe || d.Stage.ID != other || d.Title != "Окна" {
		t.Fatalf("the deal: %+v", d)
	}
	obs, err := cs.Observers(ctx, "deal", deal)
	must(err)
	if got := crm.ObserverIDs(obs); len(got) != 1 || got[0] != ann {
		t.Fatalf("the observers: %v", got)
	}
	if obs, _ = cs.Observers(ctx, "deal", copied); !slices.Equal(crm.ObserverIDs(obs), []string{bob}) {
		t.Fatalf("the copy's observers: %+v", obs)
	}
	acts, err := cs.Activities(ctx, "deal", deal)
	must(err)
	open := 0
	for _, a := range acts {
		if a.DoneAt == nil {
			open++
			if a.Kind != "meeting" {
				t.Fatalf("an open %s", a.Kind)
			}
		}
	}
	if open != 1 {
		t.Fatalf("open deeds: %d of %d", open, len(acts))
	}

	// a template of no card: the deal by a link, all its deeds; a card that is not a deal fails the step
	any, err := s.SaveTemplate(ctx, boss, Template{Name: "По ссылке", Active: true, Fields: []Field{{Key: "deal", Label: "Сделка", Kind: "string"}},
		Steps: []Step{{Kind: "crmclose", Entity: "deal", Value: "{deal}"}, {Kind: "crmmove", Stage: other, Value: "/crm/leads/{deal}"}}})
	must(err)
	id, err = s.Start(ctx, boss, any, "", map[string]string{"deal": deal})
	must(err)
	x, err = s.instance(ctx, id)
	must(err)
	acts, _ = cs.Activities(ctx, "deal", deal)
	if x.Status != "failed" || x.Step != 1 || slices.ContainsFunc(acts, func(a crm.Activity) bool { return a.DoneAt == nil }) {
		t.Fatalf("status %s at %d, deeds %+v", x.Status, x.Step, acts)
	}
}
