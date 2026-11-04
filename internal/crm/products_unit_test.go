package crm

import (
	"errors"
	"math"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestProductService_Unit(t *testing.T) {
	if !(Product{Kind: KindService}).Service() {
		t.Error("expected KindService product to report Service() = true")
	}
	if (Product{Kind: KindProduct}).Service() {
		t.Error("expected KindProduct product to report Service() = false")
	}
	if (Product{Kind: ""}).Service() {
		t.Error("expected empty kind product to report Service() = false")
	}
}

func TestRound2_Unit(t *testing.T) {
	cases := []struct {
		v, want float64
	}{
		{12.345, 12.35},
		{12.344, 12.34},
		{-12.345, -12.35},
		{0.001, 0.0},
		{100.0, 100.0},
	}
	for _, c := range cases {
		got := round2(c.v)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("round2(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestProductRow_Calculations_Unit(t *testing.T) {
	freePos := 5.0
	freeZero := 0.0
	freeNeg := -1.0
	vat20 := 20.0
	vat10 := 10.0

	tests := []struct {
		name      string
		row       ProductRow
		wantShort bool
		wantNet   float64
		wantVAT   float64
		wantTotal float64
	}{
		{
			name: "basic row without vat or discount",
			row: ProductRow{
				Price:    100,
				Quantity: 2,
			},
			wantShort: false,
			wantNet:   200,
			wantVAT:   0,
			wantTotal: 200,
		},
		{
			name: "row with discount and vat excluded",
			row: ProductRow{
				Price:       100,
				Quantity:    2,
				Discount:    10, // 200 * 0.9 = 180 Net
				VATRate:     &vat20,
				VATIncluded: false, // VAT = 180 * 0.20 = 36, Total = 216
			},
			wantShort: false,
			wantNet:   180,
			wantVAT:   36,
			wantTotal: 216,
		},
		{
			name: "row with vat included",
			row: ProductRow{
				Price:       120,
				Quantity:    1,
				VATRate:     &vat20,
				VATIncluded: true, // VAT = 120 * 20 / 120 = 20, Total = Net = 120
			},
			wantShort: false,
			wantNet:   120,
			wantVAT:   20,
			wantTotal: 120,
		},
		{
			name: "row with negative free is short",
			row: ProductRow{
				Price:    50,
				Quantity: 3,
				Free:     &freeNeg,
			},
			wantShort: true,
			wantNet:   150,
			wantVAT:   0,
			wantTotal: 150,
		},
		{
			name: "row with zero free is not short",
			row: ProductRow{
				Price:    50,
				Quantity: 1,
				Free:     &freeZero,
			},
			wantShort: false,
			wantNet:   50,
			wantVAT:   0,
			wantTotal: 50,
		},
		{
			name: "row with positive free is not short",
			row: ProductRow{
				Price:    50,
				Quantity: 1,
				Free:     &freePos,
			},
			wantShort: false,
			wantNet:   50,
			wantVAT:   0,
			wantTotal: 50,
		},
		{
			name: "row with fractional rounding and 10% vat included",
			row: ProductRow{
				Price:       99.99,
				Quantity:    3,
				Discount:    5, // 99.99 * 3 * 0.95 = 284.9715 -> Net 284.97
				VATRate:     &vat10,
				VATIncluded: true, // 284.97 * 10 / 110 = 25.9063 -> VAT 25.91, Total = 284.97
			},
			wantShort: false,
			wantNet:   284.97,
			wantVAT:   25.91,
			wantTotal: 284.97,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.row.Short(); got != tt.wantShort {
				t.Errorf("Short() = %v, want %v", got, tt.wantShort)
			}
			if got := tt.row.Net(); math.Abs(got-tt.wantNet) > 1e-6 {
				t.Errorf("Net() = %v, want %v", got, tt.wantNet)
			}
			if got := tt.row.VAT(); math.Abs(got-tt.wantVAT) > 1e-6 {
				t.Errorf("VAT() = %v, want %v", got, tt.wantVAT)
			}
			if got := tt.row.Total(); math.Abs(got-tt.wantTotal) > 1e-6 {
				t.Errorf("Total() = %v, want %v", got, tt.wantTotal)
			}
		})
	}
}

func TestRowsTotal_Unit(t *testing.T) {
	vat20 := 20.0
	rows := []ProductRow{
		{
			Price:       100,
			Quantity:    2,
			VATRate:     &vat20,
			VATIncluded: false, // Total: 240, VAT: 40
		},
		{
			Price:       120,
			Quantity:    1,
			VATRate:     &vat20,
			VATIncluded: true, // Total: 120, VAT: 20
		},
	}

	total, vat := RowsTotal(rows)
	if math.Abs(total-360) > 1e-6 {
		t.Errorf("RowsTotal total = %v, want 360", total)
	}
	if math.Abs(vat-60) > 1e-6 {
		t.Errorf("RowsTotal vat = %v, want 60", vat)
	}

	// Empty rows
	tEmpty, vEmpty := RowsTotal(nil)
	if tEmpty != 0 || vEmpty != 0 {
		t.Errorf("RowsTotal(nil) = (%v, %v), want (0, 0)", tEmpty, vEmpty)
	}
}

func TestRowsAmount_Unit(t *testing.T) {
	sql := rowsAmount("deal", "$5")
	want := `CASE WHEN EXISTS (SELECT 1 FROM crm_product_rows r WHERE r.entity = 'deal' AND r.entity_id = crm_deals.id) THEN amount ELSE $5 END`
	if sql != want {
		t.Errorf("rowsAmount() = %q, want %q", sql, want)
	}
}

func TestCleanProduct_Unit(t *testing.T) {
	validPrice := 1500.0
	negPrice := -10.0
	hugePrice := 2e15
	validVAT := 20.0
	negVAT := -5.0
	hugeVAT := 105.0

	tests := []struct {
		name    string
		prod    Product
		wantErr string
		check   func(p Product)
	}{
		{
			name: "valid product with empty kind defaults to KindProduct",
			prod: Product{
				Name:  "  Ноутбук  ",
				SKU:   "  NB-001  ",
				Price: 50000,
			},
			wantErr: "",
			check: func(p Product) {
				if p.Kind != KindProduct {
					t.Errorf("expected Kind to default to %q, got %q", KindProduct, p.Kind)
				}
				if p.Name != "Ноутбук" || p.SKU != "NB-001" {
					t.Errorf("fields not trimmed: name=%q, sku=%q", p.Name, p.SKU)
				}
			},
		},
		{
			name: "valid service",
			prod: Product{
				Kind:  KindService,
				Name:  "Доставка",
				Price: 500,
			},
			wantErr: "",
			check: func(p Product) {
				if p.Kind != KindService {
					t.Errorf("expected KindService, got %q", p.Kind)
				}
			},
		},
		{
			name: "invalid kind",
			prod: Product{
				Kind: "invalid_kind",
				Name: "Товар",
			},
			wantErr: "err.crm_product_kind",
		},
		{
			name: "empty name",
			prod: Product{
				Name: "   ",
			},
			wantErr: "err.crm_product_name",
		},
		{
			name: "name exceeds 300 runes",
			prod: Product{
				Name: strings.Repeat("n", 301),
			},
			wantErr: "err.crm_product_name",
		},
		{
			name: "negative price",
			prod: Product{
				Name:  "Товар",
				Price: -1,
			},
			wantErr: "err.crm_amount",
		},
		{
			name: "huge price",
			prod: Product{
				Name:  "Товар",
				Price: 2e15,
			},
			wantErr: "err.crm_amount",
		},
		{
			name: "negative purchase price",
			prod: Product{
				Name:          "Товар",
				PurchasePrice: &negPrice,
			},
			wantErr: "err.crm_amount",
		},
		{
			name: "huge purchase price",
			prod: Product{
				Name:          "Товар",
				PurchasePrice: &hugePrice,
			},
			wantErr: "err.crm_amount",
		},
		{
			name: "valid purchase price",
			prod: Product{
				Name:          "Товар",
				PurchasePrice: &validPrice,
				VATRate:       &validVAT,
			},
			wantErr: "",
		},
		{
			name: "negative vat rate",
			prod: Product{
				Name:    "Товар",
				VATRate: &negVAT,
			},
			wantErr: "err.crm_vat",
		},
		{
			name: "vat rate over 100",
			prod: Product{
				Name:    "Товар",
				VATRate: &hugeVAT,
			},
			wantErr: "err.crm_vat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.prod
			err := cleanProduct(&p)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("cleanProduct() unexpected error: %v", err)
				}
				if tt.check != nil {
					tt.check(p)
				}
			} else {
				if err == nil {
					t.Fatalf("cleanProduct() expected error %s, got nil", tt.wantErr)
				}
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != tt.wantErr {
					t.Errorf("cleanProduct() got error key %v, want %s", err, tt.wantErr)
				}
			}
		})
	}
}

func TestVariantName_Unit(t *testing.T) {
	props := []Prop{
		{ID: "p1", Name: "Цвет", Variant: true, Kind: PropList},
		{ID: "p2", Name: "Размер", Variant: true, Kind: PropList},
		{ID: "p3", Name: "С гравировкой", Variant: true, Kind: PropBool},
		{ID: "p4", Name: "Производитель", Variant: false, Kind: PropList}, // not a variant property
	}

	tests := []struct {
		name    string
		product string
		values  map[string]string
		want    string
	}{
		{
			name:    "multiple variant properties",
			product: "Футболка",
			values:  map[string]string{"p1": "Красный", "p2": "XL", "p4": "Nike"},
			want:    "Футболка (Красный, XL)",
		},
		{
			name:    "bool property set to 1 uses property name",
			product: "Кольцо",
			values:  map[string]string{"p3": "1"},
			want:    "Кольцо (С гравировкой)",
		},
		{
			name:    "bool property set to 0 is ignored",
			product: "Кольцо",
			values:  map[string]string{"p3": "0"},
			want:    "Кольцо",
		},
		{
			name:    "only non-variant property present returns product name",
			product: "Кроссовки",
			values:  map[string]string{"p4": "Adidas"},
			want:    "Кроссовки",
		},
		{
			name:    "empty values map",
			product: "Кепка",
			values:  map[string]string{},
			want:    "Кепка",
		},
		{
			name:    "blank property values are skipped",
			product: "Часы",
			values:  map[string]string{"p1": "   ", "p2": "Золотые"},
			want:    "Часы (Золотые)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VariantName(tt.product, props, tt.values)
			if got != tt.want {
				t.Errorf("VariantName() = %q, want %q", got, tt.want)
			}
		})
	}
}
