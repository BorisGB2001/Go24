package sign

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestSign: the signers sign in turn — the employee, then the client with the code sent to them (a wrong code counts,
// CodeTries of them end the code, an expired one does not do); signed by all, the document is done. A refusal ends
// the document, as does recalling it; the lists by filter.
func TestSign(t *testing.T) {
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
	var anna, bob, file string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, content_type, size, uploaded_by) VALUES ('k', 'd.docx', 'x', 1, $1) RETURNING id`, anna).Scan(&file))
	s := NewStore(pool)

	if _, err := s.Create(ctx, Doc{Title: "Договор", FileID: file, AuthorID: anna}, []Party{{Name: "Иван"}}); !errors.Is(err, ErrParties) {
		t.Fatalf("a client with no e-mail: %v", err)
	}
	if _, err := s.Create(ctx, Doc{Title: " ", FileID: file, AuthorID: anna}, []Party{{UserID: bob, Name: "Боб"}}); !errors.Is(err, ErrTitle) {
		t.Fatalf("no title: %v", err)
	}
	id, err := s.Create(ctx, Doc{Title: "Договор", FileID: file, Hash: Hash([]byte("x")), AuthorID: anna},
		[]Party{{UserID: bob, Name: "Боб"}, {Name: "Иван", Email: "ivan@t"}})
	must(err)
	d, err := s.Get(ctx, id)
	must(err)
	emp, cli := d.Parties[0], d.Parties[1]
	if d.Status != "signing" || d.Current().ID != emp.ID || cli.Status != "wait" || cli.Token == "" || emp.Token != "" ||
		!d.Sees(anna) || !d.Sees(bob) {
		t.Fatalf("created: %+v", d)
	}
	if list, err := s.List(ctx, bob, "me", false); err != nil || len(list) != 1 {
		t.Fatalf("bob waits: %v %v", list, err)
	}
	if n, err := s.Waiting(ctx, bob); err != nil || n != 1 {
		t.Fatalf("waiting: %d %v", n, err)
	}
	if list, err := s.List(ctx, anna, "me", false); err != nil || len(list) != 0 {
		t.Fatalf("anna does not sign: %v %v", list, err)
	}

	// not the client's turn yet
	if err := s.SetCode(ctx, cli.ID, "123456"); !errors.Is(err, ErrTurn) {
		t.Fatalf("code before the turn: %v", err)
	}
	if _, _, err := s.Sign(ctx, id, cli.ID, "", "1.1.1.1", "ua"); !errors.Is(err, ErrTurn) {
		t.Fatalf("client before the turn: %v", err)
	}
	next, done, err := s.Sign(ctx, id, emp.ID, "", "10.0.0.1", "Firefox")
	must(err)
	if done || next == nil || next.ID != cli.ID || next.Token != cli.Token {
		t.Fatalf("after the employee: %+v %v", next, done)
	}

	// the client: no code yet, a wrong one, then the right one
	if _, _, err := s.Sign(ctx, id, cli.ID, "", "1.1.1.1", "ua"); !errors.Is(err, ErrTries) {
		t.Fatalf("no code sent: %v", err)
	}
	must(s.SetCode(ctx, cli.ID, "123456"))
	if err := s.SetCode(ctx, cli.ID, "999999"); !errors.Is(err, ErrAgain) {
		t.Fatalf("a new code at once: %v", err)
	}
	if _, _, err := s.Sign(ctx, id, cli.ID, "000000", "1.1.1.1", "ua"); !errors.Is(err, ErrCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sign_parties SET code_until = now() - interval '1 second' WHERE id = $1`, cli.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Sign(ctx, id, cli.ID, "123456", "1.1.1.1", "ua"); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	must(s.SetCode(ctx, cli.ID, "654321")) // the old one has expired
	for i := 0; i < CodeTries-1; i++ {
		if _, _, err := s.Sign(ctx, id, cli.ID, "111111", "1.1.1.1", "ua"); !errors.Is(err, ErrCode) {
			t.Fatalf("wrong %d: %v", i, err)
		}
	}
	if _, _, err := s.Sign(ctx, id, cli.ID, "111111", "1.1.1.1", "ua"); !errors.Is(err, ErrTries) {
		t.Fatalf("the last try: %v", err)
	}
	if _, _, err := s.Sign(ctx, id, cli.ID, "654321", "1.1.1.1", "ua"); !errors.Is(err, ErrTries) {
		t.Fatalf("the right code after the tries: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sign_parties SET code_until = now() WHERE id = $1`, cli.ID); err != nil {
		t.Fatal(err)
	}
	must(s.SetCode(ctx, cli.ID, "777777"))
	next, done, err = s.Sign(ctx, id, cli.ID, " 777777 ", "1.1.1.1", "Safari")
	must(err)
	if !done || next != nil {
		t.Fatalf("signed by all: %+v %v", next, done)
	}
	d, p, err := s.ByToken(ctx, cli.Token)
	must(err)
	if d.Status != "done" || d.DoneAt == nil || p.Status != "signed" || p.SignedAt == nil || p.IP != "1.1.1.1" || p.UserAgent != "Safari" ||
		d.Parties[0].IP != "10.0.0.1" || d.Current() != nil {
		t.Fatalf("done: %+v %+v", d, p)
	}
	if _, _, err := s.ByToken(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no token: %v", err)
	}
	if list, err := s.List(ctx, anna, "done", false); err != nil || len(list) != 1 || len(list[0].Parties) != 2 {
		t.Fatalf("done list: %v %v", list, err)
	}
	if err := s.Cancel(ctx, id, ""); !errors.Is(err, ErrTurn) {
		t.Fatalf("recall a signed one: %v", err)
	}
	// the history: made, viewed once, signed by both, done; a view of a signer again is not kept
	must(s.Log(ctx, id, cli.ID, "", EvViewed, true, "1.1.1.1", ""))
	must(s.Log(ctx, id, cli.ID, "", EvViewed, true, "1.1.1.1", ""))
	must(s.Log(ctx, id, cli.ID, "", EvSent, false, "", ""))
	evs, err := s.Events(ctx, id)
	must(err)
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, ","); got != "created,signed,signed,done,viewed,sent" || evs[0].Actor != "Анна" || evs[1].IP != "10.0.0.1" ||
		evs[2].Party != cli.Name || evs[5].OK {
		t.Fatalf("history: %s %+v", got, evs)
	}

	// a refusal, a recall
	id2, err := s.Create(ctx, Doc{Title: "Акт", FileID: file, AuthorID: anna, Entity: "deal", EntityID: anna}, []Party{{UserID: bob, Name: "Боб"}})
	must(err)
	d2, err := s.Get(ctx, id2)
	must(err)
	must(s.Refuse(ctx, id2, d2.Parties[0].ID, "не тот срок", "", ""))
	if d2, _ = s.Get(ctx, id2); d2.Status != "refused" || d2.Parties[0].Reason != "не тот срок" {
		t.Fatalf("refused: %+v", d2)
	}
	id3, err := s.Create(ctx, Doc{Title: "Счёт", FileID: file, AuthorID: anna}, []Party{{UserID: bob, Name: "Боб"}})
	must(err)
	must(s.Cancel(ctx, id3, bob))
	if evs, err := s.Events(ctx, id3); err != nil || len(evs) != 2 || evs[1].Kind != EvCancelled || evs[1].Actor != "Боб" {
		t.Fatalf("recalled history: %+v %v", evs, err)
	}
	if evs, err := s.Events(ctx, id2); err != nil || len(evs) != 2 || evs[1].Kind != EvRefused || evs[1].Note != "не тот срок" {
		t.Fatalf("refused history: %+v %v", evs, err)
	}
	d3, _ := s.Get(ctx, id3)
	if _, _, err := s.Sign(ctx, id3, d3.Parties[0].ID, "", "", ""); !errors.Is(err, ErrTurn) {
		t.Fatalf("sign a recalled one: %v", err)
	}
	if list, err := s.List(ctx, bob, "closed", false); err != nil || len(list) != 2 {
		t.Fatalf("closed: %v %v", list, err)
	}
	if list, err := s.List(ctx, bob, "me", false); err != nil || len(list) != 0 {
		t.Fatalf("nothing to sign: %v %v", list, err)
	}
	var carol string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('carol', 'Кэрол', 'c@t') RETURNING id`).Scan(&carol))
	if list, err := s.List(ctx, carol, "", false); err != nil || len(list) != 0 {
		t.Fatalf("another employee: %v %v", list, err)
	}

	// a reviewer approves before the signer, with no code; an observer sees the document, signs nothing
	if _, err := s.Create(ctx, Doc{Title: "Акт", FileID: file, AuthorID: anna}, []Party{{Name: "Иван", Email: "ivan@t", Role: "reviewer"}}); !errors.Is(err, ErrParties) {
		t.Fatalf("a client reviewer: %v", err)
	}
	id4, err := s.Create(ctx, Doc{Title: "Договор поставки", FileID: file, AuthorID: anna, Observers: []Observer{{UserID: carol}}},
		[]Party{{UserID: bob, Name: "Боб", Role: "reviewer"}, {UserID: anna, Name: "Анна"}})
	must(err)
	d4, err := s.Get(ctx, id4)
	must(err)
	if !d4.Parties[0].Reviewer() || d4.Parties[1].Reviewer() || len(d4.Observers) != 1 || d4.Observers[0].Name != "Кэрол" ||
		!d4.Sees(carol) || d4.Current().UserID != bob {
		t.Fatalf("with a reviewer: %+v", d4)
	}
	if list, err := s.List(ctx, carol, "", false); err != nil || len(list) != 1 || list[0].ID != id4 || len(list[0].Observers) != 1 {
		t.Fatalf("the observer's list: %v %v", list, err)
	}
	if list, err := s.List(ctx, carol, "me", false); err != nil || len(list) != 0 {
		t.Fatalf("the observer signs nothing: %v %v", list, err)
	}
	if next, done, err := s.Sign(ctx, id4, d4.Parties[0].ID, "", "10.0.0.2", ""); err != nil || done || next.UserID != anna {
		t.Fatalf("approved: %v %v %v", next, done, err)
	}
	id5, err := s.Create(ctx, Doc{Title: "Договор аренды", FileID: file, AuthorID: anna},
		[]Party{{UserID: bob, Name: "Боб", Role: "reviewer"}, {UserID: anna, Name: "Анна"}})
	must(err)
	d5, _ := s.Get(ctx, id5)
	must(s.Refuse(ctx, id5, d5.Parties[0].ID, "цена", "", ""))
	if d5, _ = s.Get(ctx, id5); d5.Status != "refused" || d5.Parties[1].Status != "wait" {
		t.Fatalf("rejected: %+v", d5)
	}
	evs4, _ := s.Events(ctx, id4)
	evs5, _ := s.Events(ctx, id5)
	if len(evs4) != 2 || evs4[1].Kind != EvApproved || len(evs5) != 2 || evs5[1].Kind != EvRejected || evs5[1].Note != "цена" {
		t.Fatalf("reviewer history: %+v %+v", evs4, evs5)
	}
	if list, err := s.List(ctx, carol, "", true); err != nil || len(list) != 5 {
		t.Fatalf("admin: %v %v", list, err)
	}
	if m, err := s.OfCard(ctx, "deal", anna); err != nil || len(m) != 0 {
		t.Fatalf("of card with no card document: %v %v", m, err)
	}
}
