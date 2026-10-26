package crm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestStockRights: without roles a person has the default role's rights — reading every kind of document; an
// administrator has every right; the storekeeper role keeps the documents and sees the purchase prices; a role of
// some kinds and warehouses sees only those documents that have one of its warehouses, and changes, conducts and
// deletes only those whose warehouses are all its own (a row with no warehouse — the default one); reading comes
// with any other action; the default role can't be given or deleted; a warehouse's storekeeper without a role of the
// section gets the storekeeper one.
func TestStockRights(t *testing.T) {
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
	errKey := func(err error, key string) {
		t.Helper()
		if e, ok := err.(*i18n.Error); !ok || e.Key != key {
			t.Fatalf("want %s, got %v", key, err)
		}
	}
	var anna, bob, carl string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'bob', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('carl', 'carl', 'carl@t') RETURNING id`).Scan(&carl))
	s := NewStore(pool)

	main, err := s.SaveWarehouse(ctx, Warehouse{Name: "Основной"})
	must(err)
	north, err := s.SaveWarehouse(ctx, Warehouse{Name: "Север"})
	must(err)
	pen, err := s.SaveProduct(ctx, anna, Product{Name: "Ручка", Price: 50})
	must(err)

	// the default role: reading only
	r, err := s.StockRightsOf(ctx, anna, false)
	must(err)
	for _, k := range StockKinds {
		if !r.Can(k, "read") || r.Can(k, "write") {
			t.Fatalf("everyone %s: %+v", k, r)
		}
	}
	if r.CanAny("write") || r.PurchasePrice || r.Settings || r.Limited() || r.Kinds("read") != nil {
		t.Fatalf("everyone %+v", r)
	}
	if err := s.CheckStockDoc(ctx, r, "", "arrival", "write", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("everyone writes: %v", err)
	}
	if r, _ := s.StockRightsOf(ctx, anna, true); !r.Can("shipment", "delete") || !r.PurchasePrice || !r.Settings {
		t.Fatalf("admin %+v", r)
	}

	roles, err := s.StockRoles(ctx)
	must(err)
	if len(roles) != 2 || !roles[0].Default || roles[0].Code != "everyone" || roles[1].Code != "storekeeper" {
		t.Fatalf("roles %+v", roles)
	}
	errKey(s.DeleteStockRole(ctx, roles[0].ID), "err.crm_role_default")
	if err := s.SetStockMember(ctx, roles[0].ID, bob, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a member of the default role: %v", err)
	}
	must(s.SetStockMember(ctx, roles[1].ID, bob, true))
	r, err = s.StockRightsOf(ctx, bob, false)
	must(err)
	if !r.Can("deduct", "cancel") || !r.PurchasePrice || r.Settings || r.Limited() {
		t.Fatalf("storekeeper %+v", r)
	}

	// a role of arrivals on the north warehouse; reading comes with writing
	if _, err := s.SaveStockRole(ctx, StockRole{Name: "x", Perms: StockPerms{Warehouses: []string{anna}}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown warehouse: %v", err)
	}
	role, err := s.SaveStockRole(ctx, StockRole{Name: "Север", Perms: StockPerms{Docs: map[string][]string{"arrival": {"write", "conduct"}, "bogus": {"read"}},
		Warehouses: []string{north}}})
	must(err)
	must(s.SetStockMember(ctx, role, carl, true))
	roles, err = s.StockRoles(ctx)
	must(err)
	if p := roles[2].Perms; len(roles) != 3 || !p.Has("arrival", "read") || p.Docs["bogus"] != nil || len(roles[2].Members) != 1 {
		t.Fatalf("role %+v", roles[2])
	}
	r, err = s.StockRightsOf(ctx, carl, false)
	must(err)
	if !r.Can("arrival", "write") || r.Can("deduct", "read") || r.Can("arrival", "cancel") || !r.Limited() || !r.Warehouse(north) || r.Warehouse(main) {
		t.Fatalf("north %+v", r)
	}
	if k := r.Kinds("read"); len(k) != 1 || k[0] != "arrival" {
		t.Fatalf("north kinds %v", k)
	}
	must(s.CheckStockDoc(ctx, r, "", "arrival", "write", []StockRow{{ProductID: pen, Quantity: 1, To: north}}))
	errKey(s.CheckStockDoc(ctx, r, "", "arrival", "write", []StockRow{{ProductID: pen, Quantity: 1}}), "err.stock_warehouse_denied")
	errKey(s.CheckStockDoc(ctx, r, "", "arrival", "write", []StockRow{{ProductID: pen, Quantity: 1, To: main}}), "err.stock_warehouse_denied")

	mine, err := s.SaveStockDoc(ctx, carl, StockDoc{Kind: "arrival", Rows: []StockRow{{ProductID: pen, Quantity: 5, To: north}}})
	must(err)
	both, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "arrival", Rows: []StockRow{{ProductID: pen, Quantity: 5, To: north}, {ProductID: pen, Quantity: 1, To: main}}})
	must(err)
	other, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "arrival", Rows: []StockRow{{ProductID: pen, Quantity: 5, To: main}}})
	must(err)
	_, err = s.SaveStockDoc(ctx, anna, StockDoc{Kind: "deduct", Rows: []StockRow{{ProductID: pen, Quantity: 1, From: north}}})
	must(err)
	list, err := s.StockDocs(ctx, StockFilter{Kinds: r.Kinds("read"), Warehouses: r.WarehouseIDs()})
	must(err)
	seen := map[string]bool{}
	for _, d := range list {
		seen[d.ID] = true
	}
	if len(list) != 2 || !seen[mine] || !seen[both] {
		t.Fatalf("north sees %+v", list)
	}
	if all, _ := s.StockDocs(ctx, StockFilter{}); len(all) != 4 {
		t.Fatalf("all %d", len(all))
	}
	must(s.CheckStockDoc(ctx, r, mine, "", "conduct", nil))
	if err := s.CheckStockDoc(ctx, r, mine, "", "delete", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("north deletes: %v", err)
	}
	must(s.CheckStockDoc(ctx, r, both, "", "read", nil))
	if err := s.CheckStockDoc(ctx, r, both, "", "conduct", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("north conducts the main warehouse's: %v", err)
	}
	if err := s.CheckStockDoc(ctx, r, other, "", "read", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("north reads the main warehouse's: %v", err)
	}

	// without roles again — the default rights; a deleted role takes its people
	must(s.SetStockMember(ctx, role, carl, false))
	if r, _ := s.StockRightsOf(ctx, carl, false); r.CanAny("write") || !r.Can("deduct", "read") {
		t.Fatalf("carl without roles %+v", r)
	}
	// the storekeeper of a warehouse gets the «Кладовщик» role, unless they have a role of the section
	_, err = s.SaveWarehouse(ctx, Warehouse{ID: north, Name: "Север", Responsible: Ref{ID: carl}, Active: true})
	must(err)
	if r, _ := s.StockRightsOf(ctx, carl, false); !r.Can("deduct", "conduct") || !r.PurchasePrice {
		t.Fatalf("carl the storekeeper %+v", r)
	}
	must(s.SetStockMember(ctx, role, anna, true))
	_, err = s.SaveWarehouse(ctx, Warehouse{ID: main, Name: "Основной", Responsible: Ref{ID: anna}, Default: true, Active: true})
	must(err)
	if r, _ := s.StockRightsOf(ctx, anna, false); r.Can("deduct", "read") {
		t.Fatalf("anna keeps her role %+v", r)
	}
	must(s.DeleteStockRole(ctx, roles[1].ID))
	if r, _ := s.StockRightsOf(ctx, bob, false); r.CanAny("write") || r.PurchasePrice {
		t.Fatalf("bob without roles %+v", r)
	}
}
