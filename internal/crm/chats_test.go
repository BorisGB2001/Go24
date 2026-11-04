package crm

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestCRMChats (GO24DEV-923): a card has one «Чат» — the first tie wins and writes the history, a second is refused;
// the chat is for the maker, the responsible and the observers; a new responsible or observer asks to join it; the
// card's title names it; a deleted card takes its chat.
func TestCRMChats(t *testing.T) {
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
	var joins []string
	s.ChatJoin = func(_ context.Context, entity, id string) { joins = append(joins, entity+":"+id) }
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка с сайта", Responsible: Ref{ID: boris}})
	must(err)
	contact, err := s.SaveContact(ctx, anna, Contact{Name: "Иван Петров", Responsible: Ref{ID: anna}})
	must(err)

	if title, err := s.CardTitle(ctx, "lead", lead); err != nil || title != "Заявка с сайта" {
		t.Fatalf("the lead's title: %q %v", title, err)
	}
	if title, err := s.CardTitle(ctx, "contact", contact); err != nil || title != "Иван Петров" {
		t.Fatalf("the contact's name: %q %v", title, err)
	}
	if room, err := s.ChatOf(ctx, "lead", lead); err != nil || room != "" {
		t.Fatalf("no chat yet: %q %v", room, err)
	}
	if people, err := s.ChatPeople(ctx, "lead", lead); err != nil || !slices.Equal(people, []string{boris}) {
		t.Fatalf("people before the chat: %v %v", people, err)
	}

	made, err := s.SetChat(ctx, vera, "lead", lead, "!a:t")
	must(err)
	if !made {
		t.Fatal("the first chat is not tied")
	}
	if made, err := s.SetChat(ctx, anna, "lead", lead, "!b:t"); err != nil || made {
		t.Fatalf("a second chat tied: %v %v", made, err)
	}
	if room, _ := s.ChatOf(ctx, "lead", lead); room != "!a:t" {
		t.Fatalf("the card's chat: %q", room)
	}
	ev, err := s.Timeline(ctx, "lead", lead)
	must(err)
	n := 0
	for _, e := range ev {
		if e.Kind == "chat" {
			n++
			if e.Body != "!a:t" || e.AuthorID != vera {
				t.Fatalf("the chat line: %+v", e)
			}
		}
	}
	if n != 1 {
		t.Fatalf("chat lines: %d", n)
	}

	// observers and a new responsible: they are the chat's people, and the change asks to bring them in
	l, err := s.Lead(ctx, lead)
	must(err)
	l.Responsible, l.Observers = Ref{ID: anna}, []Ref{{ID: boris}}
	_, err = s.SaveLead(ctx, anna, l)
	must(err)
	if people, err := s.ChatPeople(ctx, "lead", lead); err != nil || !slices.Equal(people, []string{vera, anna, boris}) {
		t.Fatalf("the chat's people: %v %v", people, err)
	}
	if !slices.Contains(joins, "lead:"+lead) {
		t.Fatalf("no join asked: %v", joins)
	}
	joins = nil
	l, err = s.Lead(ctx, lead)
	must(err)
	l.Title = "Заявка с сайта (2)"
	_, err = s.SaveLead(ctx, anna, l)
	must(err)
	if len(joins) != 0 {
		t.Fatalf("a title change asked to join: %v", joins)
	}

	// a contact has no observers: its responsible
	if people, err := s.ChatPeople(ctx, "contact", contact); err != nil || !slices.Equal(people, []string{anna}) {
		t.Fatalf("the contact's chat people: %v %v", people, err)
	}
	if _, err := s.CardTitle(ctx, "quote", lead); err == nil {
		t.Fatal("a title of an unknown kind")
	}

	// a deleted card takes its chat's tie
	must2 := func(_ any, err error) { must(err) }
	must2(pool.Exec(ctx, `DELETE FROM crm_leads WHERE id = $1`, lead))
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_chats`).Scan(&left))
	if left != 0 {
		t.Fatalf("chats left: %d", left)
	}
}
