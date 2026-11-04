package sign

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestSignBlocks: the sender puts blocks of the signers on the pages instead of those there were; one of a reviewer,
// of no signer, of a wrong box or kind is refused; once a signer has signed (not a reviewer approving) — no more.
func TestSignBlocks(t *testing.T) {
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
	var anna, bob, rita, file string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('rita', 'Рита', 'rita@t') RETURNING id`).Scan(&rita))
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, content_type, size, uploaded_by) VALUES ('k', 'd.pdf', 'x', 1, $1) RETURNING id`, anna).Scan(&file))
	s := NewStore(pool)
	id, err := s.Create(ctx, Doc{Title: "Договор", FileID: file, Hash: Hash([]byte("x")), AuthorID: anna},
		[]Party{{UserID: rita, Name: "Рита", Role: "reviewer"}, {UserID: bob, Name: "Боб"}, {Name: "Иван", Email: "ivan@t"}})
	must(err)
	d, err := s.Get(ctx, id)
	must(err)
	rev, emp, cli := d.Parties[0], d.Parties[1], d.Parties[2]

	two := []Block{
		{PartyID: cli.ID, Kind: "signature", Page: 2, X: 0.6, Y: 0.8, W: 0.25, H: 0.06},
		{PartyID: emp.ID, Kind: "text", Page: 1, X: 0.1, Y: 0.9, W: 0.3, H: 0.03, Text: "Согласовано"},
	}
	must(s.SetBlocks(ctx, id, two))
	bs, err := s.Blocks(ctx, id)
	must(err)
	if len(bs) != 2 || bs[0].Page != 1 || bs[0].Text != "Согласовано" || bs[0].PartyID != emp.ID || bs[1].Kind != "signature" ||
		bs[1].X < 0.599 || bs[1].X > 0.601 {
		t.Fatalf("blocks: %+v", bs)
	}
	for name, b := range map[string]Block{
		"reviewer":   {PartyID: rev.ID, Kind: "signature", Page: 1, X: 0.1, Y: 0.1, W: 0.2, H: 0.1},
		"no signer":  {PartyID: anna, Kind: "name", Page: 1, X: 0.1, Y: 0.1, W: 0.2, H: 0.1},
		"kind":       {PartyID: emp.ID, Kind: "qr", Page: 1, X: 0.1, Y: 0.1, W: 0.2, H: 0.1},
		"off page":   {PartyID: emp.ID, Kind: "date", Page: 1, X: 0.9, Y: 0.1, W: 0.2, H: 0.1},
		"page 0":     {PartyID: emp.ID, Kind: "date", Page: 0, X: 0.1, Y: 0.1, W: 0.2, H: 0.1},
		"empty size": {PartyID: emp.ID, Kind: "date", Page: 1, X: 0.1, Y: 0.1, W: 0, H: 0.1},
	} {
		if err := s.SetBlocks(ctx, id, []Block{b}); !errors.Is(err, ErrBlocks) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if bs, _ := s.Blocks(ctx, id); len(bs) != 2 {
		t.Fatalf("a refused change kept: %+v", bs)
	}
	// the text of a block but a text one is not kept; one block instead of two
	must(s.SetBlocks(ctx, id, []Block{{PartyID: emp.ID, Kind: "date", Page: 1, X: 0.5, Y: 0.5, W: 0.1, H: 0.03, Text: "x"}}))
	if bs, _ := s.Blocks(ctx, id); len(bs) != 1 || bs[0].Kind != "date" || bs[0].Text != "" {
		t.Fatalf("replaced: %+v", bs)
	}

	// fields: a caption needed; the signer fills in theirs, a required one left empty — refused, nothing kept
	if err := s.SetBlocks(ctx, id, []Block{{PartyID: emp.ID, Kind: "field", Page: 1, X: 0.1, Y: 0.1, W: 0.3, H: 0.03, Text: " "}}); !errors.Is(err, ErrBlocks) {
		t.Fatalf("a field with no caption: %v", err)
	}
	must(s.SetBlocks(ctx, id, []Block{
		{PartyID: emp.ID, Kind: "field", Page: 1, X: 0.1, Y: 0.2, W: 0.3, H: 0.03, Text: " ИНН ", Required: true},
		{PartyID: emp.ID, Kind: "field", Page: 1, X: 0.1, Y: 0.3, W: 0.3, H: 0.03, Text: "Телефон"},
		{PartyID: cli.ID, Kind: "field", Page: 2, X: 0.1, Y: 0.3, W: 0.3, H: 0.03, Text: "Адрес", Required: true},
		{PartyID: cli.ID, Kind: "date", Page: 2, X: 0.1, Y: 0.4, W: 0.3, H: 0.03, Required: true},
	}))
	bs, err = s.Blocks(ctx, id)
	must(err)
	mine := Fields(bs, emp.ID)
	if len(mine) != 2 || mine[0].Text != "ИНН" || !mine[0].Required || mine[1].Required || bs[3].Required {
		t.Fatalf("fields: %+v", bs)
	}
	if err := s.FillFields(ctx, id, emp.ID, map[string]string{mine[1].ID: "+7 900"}); !errors.Is(err, ErrFields) {
		t.Fatalf("a required field empty: %v", err)
	}
	if bs, _ := s.Blocks(ctx, id); Fields(bs, emp.ID)[1].Value != "" {
		t.Fatal("kept though refused")
	}
	must(s.FillFields(ctx, id, emp.ID, map[string]string{mine[0].ID: " 7701234567 ", mine[1].ID: "+7 900", Fields(bs, cli.ID)[0].ID: "чужое"}))
	bs, _ = s.Blocks(ctx, id)
	if f := Fields(bs, emp.ID); f[0].Value != "7701234567" || f[1].Value != "+7 900" || Fields(bs, cli.ID)[0].Value != "" {
		t.Fatalf("filled: %+v", bs)
	}

	// the reviewer approving does not close the blocks, the signer signing does
	if _, _, err := s.Sign(ctx, id, rev.ID, "", "1.1.1.1", "ua"); err != nil {
		t.Fatal(err)
	}
	must(s.SetBlocks(ctx, id, two))
	if _, _, err := s.Sign(ctx, id, emp.ID, "", "1.1.1.1", "ua"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBlocks(ctx, id, nil); !errors.Is(err, ErrBlocksSigned) {
		t.Fatalf("after signing: %v", err)
	}
	if bs, _ := s.Blocks(ctx, id); len(bs) != 2 {
		t.Fatalf("blocks after signing: %+v", bs)
	}
}
