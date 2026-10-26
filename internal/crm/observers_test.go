package crm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestCRMObservers: the observers of a deal and a lead see the card with "own" rights, in lists and the filter
// «Я наблюдатель»; they are notified of being made observers, of comments and stages (not of their own); the
// change is in the history; the form without the field keeps them.
func TestCRMObservers(t *testing.T) {
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
	var anna, boris, vera string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'Борис', 'boris@t') RETURNING id`).Scan(&boris))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('vera', 'Вера', 'vera@t') RETURNING id`).Scan(&vera))
	s := NewStore(pool)
	manager, err := s.SaveRole(ctx, Role{Name: "Менеджер", Perms: Perms{"deal": {"read": "own", "write": "own"}, "lead": {"read": "own", "write": "own"}}})
	must(err)
	must(s.SetMember(ctx, manager, boris, true))
	must(s.SetMember(ctx, manager, vera, true))
	pipes, err := s.Pipelines(ctx)
	must(err)
	notes := func(user, kind string) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = $2`, user, kind).Scan(&n))
		return n
	}

	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: pipes[0].ID, Observers: []Ref{{ID: boris}, {ID: anna}}})
	must(err)
	d, err := s.Deal(ctx, deal)
	must(err)
	if len(d.Observers) != 2 || d.Observers[0].Name != "Анна" || d.Observers[1].Name != "Борис" {
		t.Fatalf("observers %+v", d.Observers)
	}
	if notes(boris, "crm_observer") != 1 || notes(anna, "crm_observer") != 0 {
		t.Fatal("the new observer is told, the one who made them is not")
	}
	if err := s.CheckCard(ctx, boris, "deal", "read", deal, anna); err != nil {
		t.Fatalf("an observer cannot read the card: %v", err)
	}
	if err := s.CheckCard(ctx, boris, "deal", "write", deal, anna); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer changes the card: %v", err)
	}
	if err := s.CheckCard(ctx, vera, "deal", "read", deal, anna); !errors.Is(err, ErrForbidden) {
		t.Fatalf("not an observer reads the card: %v", err)
	}
	if list, _ := s.DealList(ctx, boris, "", Filter{}); len(list) != 1 {
		t.Fatalf("an observer's list %d", len(list))
	}
	if list, _ := s.DealList(ctx, vera, "", Filter{}); len(list) != 0 {
		t.Fatalf("another's list %d", len(list))
	}
	other, err := s.SaveDeal(ctx, anna, Deal{Title: "Другая", PipelineID: pipes[0].ID})
	must(err)
	if list, _ := s.DealList(ctx, anna, "", Filter{Observer: boris}); len(list) != 1 || list[0].ID != deal {
		t.Fatalf("filter by observer %+v", list)
	}

	// comments and stages: the observers (and for a comment the responsible) but the author
	must(s.Comment(ctx, boris, "deal", deal, "Смотрю"))
	if notes(anna, "crm_comment") != 1 || notes(boris, "crm_comment") != 0 {
		t.Fatal("a comment's notifications")
	}
	must(s.Comment(ctx, anna, "deal", other, "Без наблюдателей"))
	if notes(anna, "crm_comment") != 1 {
		t.Fatal("the author is told of their own comment")
	}
	_, err = s.MoveDeal(ctx, anna, deal, pipes[0].Stages[1].ID)
	must(err)
	if notes(boris, "crm_stage") != 1 || notes(anna, "crm_stage") != 0 {
		t.Fatal("a stage's notifications")
	}

	// the form without the field keeps them; the full list replaces them, in the history
	d.Observers = nil
	_, err = s.SaveDeal(ctx, anna, d)
	must(err)
	if d, _ = s.Deal(ctx, deal); len(d.Observers) != 2 {
		t.Fatal("observers lost by a form without them")
	}
	must(s.SetObservers(ctx, anna, "deal", deal, []string{vera}))
	if d, _ = s.Deal(ctx, deal); len(d.Observers) != 1 || d.Observers[0].ID != vera {
		t.Fatalf("observers %+v", d.Observers)
	}
	if err := s.CheckCard(ctx, boris, "deal", "read", deal, anna); !errors.Is(err, ErrForbidden) {
		t.Fatal("a former observer still reads the card")
	}
	ev, err := s.Timeline(ctx, "deal", deal)
	must(err)
	if ev[0].Kind != "changed" || len(Changes(ev[0].Body)) != 1 || Changes(ev[0].Body)[0].Field != "observers" {
		t.Fatalf("history %+v", ev[0])
	}

	// a lead the same
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Observers: []Ref{{ID: vera}}})
	must(err)
	if list, _ := s.LeadList(ctx, vera, Filter{}); len(list) != 1 || list[0].ID != lead {
		t.Fatalf("an observer's leads %+v", list)
	}
	l, err := s.Lead(ctx, lead)
	must(err)
	l.Observers = []Ref{}
	_, err = s.SaveLead(ctx, anna, l)
	must(err)
	if list, _ := s.LeadList(ctx, vera, Filter{}); len(list) != 0 {
		t.Fatal("observers kept by an empty list")
	}
	if err := s.SetObservers(ctx, anna, "contact", lead, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("a contact has observers")
	}
	must(s.DeleteDeal(ctx, anna, deal))
}
