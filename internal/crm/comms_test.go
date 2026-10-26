package crm

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

func TestNormComms(t *testing.T) {
	old := []Comm{{"phone", "mobile", "+7 900 111-22-33"}, {"phone", "home", "84951234567"}, {"email", "work", "a@x.ru"}}
	for _, c := range []struct {
		name         string
		phone, email string
		given        []Comm
		want         []Comm
	}{
		{"kept", "+7 900 111-22-33", "a@x.ru", nil, old},
		{"the main phone changed", "+7 900 999-00-00", "a@x.ru", nil,
			[]Comm{{"phone", "mobile", "+7 900 999-00-00"}, {"phone", "home", "84951234567"}, {"email", "work", "a@x.ru"}}},
		{"the main e-mail emptied", "+7 900 111-22-33", "", nil, old[:2]},
		{"the very ones given back, the main phone changed", "8 900", "a@x.ru", slices.Clone(old),
			[]Comm{{"phone", "mobile", "8 900"}, {"phone", "home", "84951234567"}, {"email", "work", "a@x.ru"}}},
		{"given: types checked, blanks and repeats dropped", "", "", []Comm{{"phone", "fax", "+79001112233"}, {"phone", "home", " "},
			{"phone", "home", "8 (900) 111-22-33"}, {"email", "home", "B@x.ru"}, {"email", "work", "b@x.ru"}, {"im", "work", "x"}},
			[]Comm{{"phone", "work", "+79001112233"}, {"email", "home", "B@x.ru"}}},
	} {
		phone, email, got, err := normComms(old, c.phone, c.email, c.given)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		// the main ones are the first of their kind (kinds keep their own order)
		if !slices.Equal(CommsOf(got, "phone"), CommsOf(c.want, "phone")) || !slices.Equal(CommsOf(got, "email"), CommsOf(c.want, "email")) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
		if ps := CommsOf(got, "phone"); (len(ps) > 0 && phone != ps[0].Value) || (len(ps) == 0 && phone != "") {
			t.Errorf("%s: main phone %q", c.name, phone)
		}
		if es := CommsOf(got, "email"); (len(es) > 0 && email != es[0].Value) || (len(es) == 0 && email != "") {
			t.Errorf("%s: main e-mail %q", c.name, email)
		}
	}
	many := make([]Comm, 11)
	for i := range many {
		many[i] = Comm{"email", "work", string(rune('a'+i)) + "@x.ru"}
	}
	if _, _, got, err := normComms(nil, "8 900", "c@x.ru", nil); err != nil || len(got) != 2 {
		t.Fatalf("a new card by its phone and e-mail: %+v %v", got, err)
	}
	var ie *i18n.Error
	if _, _, _, err := normComms(nil, "", "", many); !errors.As(err, &ie) || ie.Key != "err.crm_comms_many" {
		t.Fatalf("11 e-mails: %v", err)
	}
}

// TestComms: a card keeps several phones and e-mails (the main one in its columns); a lead, the duplicates, the list's
// search and the conversion find and carry all of them; merging keeps the other card's ones, deleting removes them.
func TestComms(t *testing.T) {
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
	me := Ref{ID: boss}

	ivan, err := s.SaveContact(ctx, boss, Contact{Name: "Иван", Responsible: me, Comms: []Comm{{"phone", "work", "+7 495 100-00-01"},
		{"phone", "mobile", "+7 900 200-00-02"}, {"email", "work", "ivan@firma.ru"}, {"email", "home", "ivan@mail.ru"}}})
	must(err)
	x, err := s.Contact(ctx, ivan)
	must(err)
	if x.Phone != "+7 495 100-00-01" || x.Email != "ivan@firma.ru" || len(x.Comms) != 4 || x.Comms[1].Type != "mobile" {
		t.Fatalf("contact: %+v", x)
	}
	// a card changed by the main phone only (as a robot or an older form does): the first replaced, the rest kept
	x.Phone, x.Comms = "+7 495 100-00-09", nil
	_, err = s.SaveContact(ctx, boss, x)
	must(err)
	if x, _ = s.Contact(ctx, ivan); x.Phone != "+7 495 100-00-09" || len(x.Comms) != 4 || x.Comms[1].Value != "+7 900 200-00-02" {
		t.Fatalf("the main phone changed: %+v", x)
	}

	// the mobile phone and the personal e-mail find him
	lead, err := s.SaveLead(ctx, boss, Lead{Title: "Звонок", Phone: "89002000002", Responsible: me})
	must(err)
	if l, _ := s.Lead(ctx, lead); !l.Repeat || l.ContactID != ivan {
		t.Fatalf("a lead from the second phone: %+v", l)
	}
	dups, err := s.Duplicates(ctx, "lead", "", "", "", "IVAN@mail.ru")
	must(err)
	if len(dups) != 1 || dups[0].ID != ivan || dups[0].By != "email" {
		t.Fatalf("duplicates by the second e-mail: %+v", dups)
	}
	found, err := s.Contacts(ctx, boss, "9002000002", "")
	must(err)
	if len(found) != 1 || found[0].ID != ivan {
		t.Fatalf("search by the second phone's digits: %+v", found)
	}

	// converting a lead with two phones: the new contact has both
	two, err := s.SaveLead(ctx, boss, Lead{Title: "Новый", Name: "Пётр", Responsible: me, Comms: []Comm{{"phone", "work", "+7 812 300-00-03"},
		{"phone", "home", "+7 812 300-00-04"}}})
	must(err)
	got, err := s.ConvertLeadTo(ctx, boss, two, ConvertTo{Contact: true})
	must(err)
	if c, _ := s.Contact(ctx, got.ContactID); len(c.Comms) != 2 || c.Comms[1].Value != "+7 812 300-00-04" || c.Comms[1].Type != "home" {
		t.Fatalf("the converted contact: %+v", c)
	}

	// merging: Пётр's phones go to Иван after his; deleting Иван removes his phones and e-mails
	must(s.Merge(ctx, boss, "contact", ivan, got.ContactID))
	if x, _ = s.Contact(ctx, ivan); len(CommsOf(x.Comms, "phone")) != 4 || x.Phone != "+7 495 100-00-09" {
		t.Fatalf("merged: %+v", x.Comms)
	}
	must(s.DeleteContact(ctx, boss, ivan))
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_comms WHERE entity = 'contact'`).Scan(&left))
	if left != 0 {
		t.Fatalf("%d phones and e-mails of deleted contacts", left)
	}
}
