package crm

import (
	"math"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestStockRowSum_Unit(t *testing.T) {
	row := StockRow{Price: 15.55, Quantity: 3}
	if got := row.Sum(); math.Abs(got-46.65) > 1e-6 {
		t.Errorf("StockRow.Sum() = %v, want 46.65", got)
	}
}

func TestStockDocEditable_Unit(t *testing.T) {
	if !(StockDoc{Status: "draft"}).Editable() {
		t.Error("draft doc should be editable")
	}
	if !(StockDoc{Status: "cancelled"}).Editable() {
		t.Error("cancelled doc should be editable")
	}
	if (StockDoc{Status: "conducted"}).Editable() {
		t.Error("conducted doc should not be editable")
	}
}

func TestValidStockKind_Unit(t *testing.T) {
	valid := []string{"arrival", "adjustment", "moving", "deduct", "shipment"}
	for _, k := range valid {
		if !ValidStockKind(k) {
			t.Errorf("ValidStockKind(%q) = false, want true", k)
		}
	}
	invalid := []string{"receipt", "inventory", "transfer", "sale", "", "other"}
	for _, k := range invalid {
		if ValidStockKind(k) {
			t.Errorf("ValidStockKind(%q) = true, want false", k)
		}
	}
}

func TestStockUsesFromAndTo_Unit(t *testing.T) {
	// usesFrom: moving, deduct, shipment
	if !usesFrom("moving") || !usesFrom("deduct") || !usesFrom("shipment") {
		t.Error("expected usesFrom to be true for moving, deduct, shipment")
	}
	if usesFrom("receipt") || usesFrom("inventory") {
		t.Error("expected usesFrom to be false for receipt, inventory")
	}

	// usesTo: receipt, moving, inventory (not deduct and not shipment)
	if !usesTo("receipt") || !usesTo("moving") || !usesTo("inventory") {
		t.Error("expected usesTo to be true for receipt, moving, inventory")
	}
	if usesTo("deduct") || usesTo("shipment") {
		t.Error("expected usesTo to be false for deduct, shipment")
	}
}

func TestStockLineFree_Unit(t *testing.T) {
	line := StockLine{Total: 10.555, Reserve: 3.222}
	if got := line.Free(); math.Abs(got-7.333) > 1e-6 {
		t.Errorf("StockLine.Free() = %v, want 7.333", got)
	}
}

func TestRound3_Unit(t *testing.T) {
	cases := []struct {
		v, want float64
	}{
		{1.2345, 1.235},
		{1.2344, 1.234},
		{-1.2345, -1.235},
		{0.0001, 0.0},
	}
	for _, c := range cases {
		got := round3(c.v)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("round3(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestSupplier_Unit(t *testing.T) {
	docCompany := StockDoc{Kind: "arrival", SupplierKind: "company", Supplier: Ref{ID: "sup-co"}}
	if got := supplier(docCompany, "company"); got == nil || *got != "sup-co" {
		t.Errorf("supplier(arrival company) = %v, want \"sup-co\"", got)
	}
	if got := supplier(docCompany, "contact"); got != nil {
		t.Errorf("supplier(arrival contact mismatch) = %v, want nil", got)
	}

	docContact := StockDoc{Kind: "arrival", SupplierKind: "contact", Supplier: Ref{ID: "sup-person"}}
	if got := supplier(docContact, "contact"); got == nil || *got != "sup-person" {
		t.Errorf("supplier(arrival contact) = %v, want \"sup-person\"", got)
	}

	docMoving := StockDoc{Kind: "moving", Supplier: Ref{ID: "sup-co"}}
	if got := supplier(docMoving, "company"); got != nil {
		t.Errorf("supplier(moving) = %v, want nil", got)
	}
}

func TestStockMoveInAndOut_Unit(t *testing.T) {
	mPos := StockMove{Quantity: 15.5}
	if mPos.In() != 15.5 || mPos.Out() != 0 {
		t.Errorf("mPos: In=%v, Out=%v, want (15.5, 0)", mPos.In(), mPos.Out())
	}

	mNeg := StockMove{Quantity: -7.25}
	if mNeg.In() != 0 || mNeg.Out() != 7.25 {
		t.Errorf("mNeg: In=%v, Out=%v, want (0, 7.25)", mNeg.In(), mNeg.Out())
	}
}

func TestCostOf_Unit(t *testing.T) {
	avgCosts := map[string]float64{
		"p1": 150.0,
	}
	pp := 200.0
	prodWithPP := Product{ID: "p2", PurchasePrice: &pp}
	prodNoPP := Product{ID: "p3", PurchasePrice: nil}

	if got := costOf(Product{ID: "p1"}, avgCosts); got != 150.0 {
		t.Errorf("costOf p1 = %v, want 150.0", got)
	}
	if got := costOf(prodWithPP, avgCosts); got != 200.0 {
		t.Errorf("costOf p2 = %v, want 200.0", got)
	}
	if got := costOf(prodNoPP, avgCosts); got != 0.0 {
		t.Errorf("costOf p3 = %v, want 0.0", got)
	}
}

func TestStockSettingsReserveUntil_Unit(t *testing.T) {
	setZero := StockSettings{ReserveDays: 0}
	if got := setZero.reserveUntil(); got != nil {
		t.Errorf("reserveUntil(0) = %v, want nil", got)
	}

	setNeg := StockSettings{ReserveDays: -3}
	if got := setNeg.reserveUntil(); got != nil {
		t.Errorf("reserveUntil(-3) = %v, want nil", got)
	}

	setDays := StockSettings{ReserveDays: 5}
	got := setDays.reserveUntil()
	if got == nil {
		t.Fatal("reserveUntil(5) = nil, want time")
	}
	y, m, d := time.Now().Date()
	want := time.Date(y, m, d+5, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("reserveUntil(5) = %v, want %v", got, want)
	}
}

func TestStockRights_Unit(t *testing.T) {
	adminRights := AllStockRights()
	if !adminRights.Can("arrival", "write") {
		t.Error("admin should have arrival write")
	}
	if !adminRights.CanAny("conduct") {
		t.Error("admin should have CanAny conduct")
	}
	if !adminRights.Warehouse("any-warehouse") {
		t.Error("admin should have access to any warehouse")
	}
	if adminRights.Limited() {
		t.Error("admin should not be limited")
	}

	// Limited rights
	limRights := StockRights{
		docs: map[string]map[string]bool{
			"arrival": {"read": true, "write": true},
			"moving":  {"read": true},
		},
		warehouses: map[string]bool{
			"w1": true,
			"w2": true,
		},
		all: false,
	}

	if !limRights.Limited() {
		t.Error("limRights should be limited")
	}
	if !limRights.Warehouse("w1") || limRights.Warehouse("w3") {
		t.Error("warehouse check failed for w1 or w3")
	}
	if !limRights.Can("arrival", "write") || limRights.Can("moving", "write") {
		t.Error("Can check failed for arrival or moving")
	}
	if !limRights.CanAny("write") || limRights.CanAny("conduct") {
		t.Error("CanAny check failed")
	}

	whIDs := limRights.WarehouseIDs()
	slices.Sort(whIDs)
	if !reflect.DeepEqual(whIDs, []string{"w1", "w2"}) {
		t.Errorf("WarehouseIDs() = %v, want [\"w1\", \"w2\"]", whIDs)
	}

	readKinds := limRights.Kinds("read")
	slices.Sort(readKinds)
	wantKinds := []string{"arrival", "moving"}
	if !reflect.DeepEqual(readKinds, wantKinds) {
		t.Errorf("Kinds(\"read\") = %v, want %v", readKinds, wantKinds)
	}

	// Doc check
	docOK := StockDoc{Kind: "arrival", Rows: []StockRow{{To: "w1"}}}
	if !limRights.Doc(docOK, "write") {
		t.Error("Doc check should succeed for allowed arrival to w1")
	}
	docBadWH := StockDoc{Kind: "arrival", Rows: []StockRow{{To: "w99"}}}
	if limRights.Doc(docBadWH, "write") {
		t.Error("Doc check should fail for arrival to unauthorized warehouse w99")
	}
	docNoPerm := StockDoc{Kind: "deduct", Rows: []StockRow{{From: "w1"}}}
	if limRights.Doc(docNoPerm, "write") {
		t.Error("Doc check should fail for deduct without permission")
	}

	// Rows check
	rowsOK := []StockRow{{From: "w1", To: "w2"}}
	if !limRights.Rows("moving", rowsOK, "") {
		t.Error("Rows check should succeed for w1 -> w2")
	}
	rowsBad := []StockRow{{From: "w1", To: "w99"}}
	if limRights.Rows("moving", rowsBad, "") {
		t.Error("Rows check should fail for w1 -> w99")
	}
}

func TestStockPerms_Unit(t *testing.T) {
	p := StockPerms{
		Docs: map[string][]string{
			"receipt": {"read", "write"},
		},
		Warehouses: []string{"w1"},
	}

	if !p.Has("receipt", "read") || p.Has("receipt", "conduct") {
		t.Error("StockPerms.Has check failed")
	}
	if !p.HasWarehouse("w1") || p.HasWarehouse("w2") {
		t.Error("StockPerms.HasWarehouse check failed")
	}
	pAll := StockPerms{Warehouses: nil} // empty warehouses means all
	if !pAll.HasWarehouse("any") {
		t.Error("StockPerms with nil Warehouses should have any warehouse")
	}
}
