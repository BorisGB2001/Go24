package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestRelations: «Связи» of smart processes — an element is bound to a parent only of a kind its smart process is
// bound to, one of each kind (a new one replaces it), never to itself or its descendant; the parent's card lists its
// children; the timeline tells binding and unbinding; taking a kind away unbinds its elements; a deleted parent
// comes back from the bin with its children; deleting a smart process forgets the relations to it.
func TestRelations(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool)

	contracts, err := s.AddType(ctx, "Договоры", "")
	must(err)
	shipments, err := s.AddType(ctx, "Поставки", "")
	must(err)
	pipe := func(typ string) string {
		p, err := s.PipelinesOf(ctx, typ)
		must(err)
		return p[0].ID
	}
	deals, err := s.Pipelines(ctx)
	must(err)
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Сделка", PipelineID: deals[0].ID})
	must(err)
	other, err := s.SaveDeal(ctx, anna, Deal{Title: "Другая", PipelineID: deals[0].ID})
	must(err)
	contract, err := s.SaveDeal(ctx, anna, Deal{Title: "Договор 1", PipelineID: pipe(contracts)})
	must(err)
	shipment, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка 1", PipelineID: pipe(shipments)})
	must(err)

	// not bound to anything yet; a deal has no parents at all
	if err := s.SetParent(ctx, anna, contract, "", deal); err == nil {
		t.Fatal("bound to a kind not chosen")
	}
	if err := s.SetTypeParents(ctx, contracts, []string{contracts}); err == nil {
		t.Fatal("a smart process bound to itself")
	}
	must(s.SetTypeParents(ctx, contracts, []string{""}))
	must(s.SetTypeParents(ctx, shipments, []string{contracts, "", contracts}))
	if k, _ := s.TypeParents(ctx, shipments); len(k) != 2 || k[0] != contracts || k[1] != "" {
		t.Fatalf("kinds of shipments: %q", k)
	}
	if ct, _ := s.ChildTypes(ctx, ""); len(ct) != 2 {
		t.Fatalf("child types of deals: %+v", ct)
	}
	if ct, _ := s.ChildTypes(ctx, contracts); len(ct) != 1 || ct[0].ID != shipments {
		t.Fatalf("child types of contracts: %+v", ct)
	}
	if err := s.SetParent(ctx, anna, deal, "", other); err == nil {
		t.Fatal("a deal bound to a deal")
	}
	if err := s.SetParent(ctx, anna, contract, "", shipment); err == nil {
		t.Fatal("bound to a card of another kind")
	}

	must(s.SetParent(ctx, anna, contract, "", deal))
	must(s.SetParent(ctx, anna, shipment, contracts, contract))
	must(s.SetParent(ctx, anna, shipment, "", deal))
	if p, _ := s.Parents(ctx, shipment); len(p) != 2 {
		t.Fatalf("parents of the shipment: %+v", p)
	}
	if c, _ := s.Children(ctx, anna, deal); len(c) != 2 {
		t.Fatalf("children of the deal: %+v", c)
	}
	// one parent of a kind: another deal replaces the first
	must(s.SetParent(ctx, anna, contract, "", other))
	if p, _ := s.Parents(ctx, contract); len(p) != 1 || p[0].ID != other {
		t.Fatalf("parent replaced: %+v", p)
	}
	if c, _ := s.Children(ctx, anna, deal); len(c) != 1 || c[0].ID != shipment {
		t.Fatalf("children after the replacement: %+v", c)
	}
	// no loops: a contract under a shipment of its own is refused
	must(s.SetTypeParents(ctx, contracts, []string{"", shipments}))
	if err := s.SetParent(ctx, anna, contract, shipments, shipment); err == nil {
		t.Fatal("a loop of parents")
	}
	if err := s.SetParent(ctx, anna, contract, "", contract); err == nil {
		t.Fatal("bound to itself")
	}
	tl, err := s.Timeline(ctx, "deal", contract)
	must(err)
	var bound int
	for _, e := range tl {
		if e.Kind == "bound" {
			bound++
		}
	}
	if bound != 2 {
		t.Fatalf("bindings in the timeline: %d", bound)
	}
	must(s.SetParent(ctx, anna, contract, "", ""))
	if p, _ := s.Parents(ctx, contract); len(p) != 0 {
		t.Fatalf("unbound: %+v", p)
	}
	if tl, _ := s.Timeline(ctx, "deal", contract); tl[0].Kind != "unbound" || tl[0].Body != "Другая" {
		t.Fatalf("unbinding in the timeline: %+v", tl[0])
	}

	// all at once (REST): one of each kind
	if err := s.SetParents(ctx, anna, shipment, []string{deal, other}); err == nil {
		t.Fatal("two parents of one kind")
	}
	must(s.SetParents(ctx, anna, shipment, []string{other}))
	if p, _ := s.Parents(ctx, shipment); len(p) != 1 || p[0].ID != other {
		t.Fatalf("parents set at once: %+v", p)
	}
	must(s.SetParents(ctx, anna, shipment, []string{deal, contract}))

	// the bin: a deleted parent comes back with its children bound
	must(s.DeleteDeal(ctx, anna, contract))
	if p, _ := s.Parents(ctx, shipment); len(p) != 1 {
		t.Fatalf("parents with the contract in the bin: %+v", p)
	}
	bin, err := s.Recycle(ctx, anna, "", "")
	must(err)
	_, _, err = s.Restore(ctx, anna, bin[0].ID)
	must(err)
	if p, _ := s.Parents(ctx, shipment); len(p) != 2 {
		t.Fatalf("parents after restoring: %+v", p)
	}

	// taking the deals away from shipments unbinds them from deals
	must(s.SetTypeParents(ctx, shipments, []string{contracts}))
	if p, _ := s.Parents(ctx, shipment); len(p) != 1 || p[0].ID != contract {
		t.Fatalf("parents after the kind is taken away: %+v", p)
	}
	// a smart process gone: the relations to it go too
	must(s.DeleteDeal(ctx, anna, shipment))
	must(s.DeleteType(ctx, shipments))
	if k, _ := s.TypeParents(ctx, contracts); len(k) != 1 || k[0] != "" {
		t.Fatalf("kinds of contracts after shipments are gone: %q", k)
	}
}
