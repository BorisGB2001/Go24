package crm

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"

	"go24/internal/i18n"
)

func TestFillImported_Unit(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		present []string
		yesVals []string
		initial Product
		wantErr string
		check   func(p Product)
	}{
		{
			name: "strings and clean prices with comma and spaces",
			values: map[string]string{
				"name":           "Стул офисный",
				"sku":            "CHR-100",
				"price":          "  1 500,50  ",
				"purchase_price": "1 000,00", // with non-breaking space
				"vat":            "20%",
				"currency":       "RUB",
				"unit":           "шт",
				"description":    "Удобный стул",
				"vat_included":   "yes",
				"active":         "1",
			},
			present: []string{"name", "sku", "price", "purchase_price", "vat", "currency", "unit", "description", "vat_included", "active"},
			yesVals: []string{"yes", "1"},
			wantErr: "",
			check: func(p Product) {
				if p.Name != "Стул офисный" || p.SKU != "CHR-100" || p.Currency != "RUB" || p.Unit != "шт" {
					t.Errorf("string fields mismatch: %+v", p)
				}
				if math.Abs(p.Price-1500.50) > 1e-6 {
					t.Errorf("Price = %v, want 1500.50", p.Price)
				}
				if p.PurchasePrice == nil || math.Abs(*p.PurchasePrice-1000.0) > 1e-6 {
					t.Errorf("PurchasePrice = %v, want 1000.0", p.PurchasePrice)
				}
				if p.VATRate == nil || math.Abs(*p.VATRate-20.0) > 1e-6 {
					t.Errorf("VATRate = %v, want 20.0", p.VATRate)
				}
				if !p.VATIncluded {
					t.Error("VATIncluded should be true")
				}
				if !p.Active {
					t.Error("Active should be true")
				}
			},
		},
		{
			name: "empty price clears to zero",
			values: map[string]string{
				"price": "",
			},
			present: []string{"price"},
			initial: Product{Price: 999},
			wantErr: "",
			check: func(p Product) {
				if p.Price != 0 {
					t.Errorf("Price = %v, want 0", p.Price)
				}
			},
		},
		{
			name: "empty purchase price and vat clear to nil",
			values: map[string]string{
				"purchase_price": "",
				"vat":            "",
			},
			present: []string{"purchase_price", "vat"},
			initial: func() Product {
				pp, vat := 100.0, 20.0
				return Product{PurchasePrice: &pp, VATRate: &vat}
			}(),
			wantErr: "",
			check: func(p Product) {
				if p.PurchasePrice != nil {
					t.Errorf("PurchasePrice = %v, want nil", p.PurchasePrice)
				}
				if p.VATRate != nil {
					t.Errorf("VATRate = %v, want nil", p.VATRate)
				}
			},
		},
		{
			name: "invalid price format returns err.crm_amount",
			values: map[string]string{
				"price": "not-a-number",
			},
			present: []string{"price"},
			wantErr: "err.crm_amount",
		},
		{
			name: "barcodes split by commas, semicolons, spaces and newlines",
			values: map[string]string{
				"barcodes": "12345, 67890; 111222\n333444   555666",
			},
			present: []string{"barcodes"},
			wantErr: "",
			check: func(p Product) {
				want := []string{"12345", "67890", "111222", "333444", "555666"}
				if !reflect.DeepEqual(p.Barcodes, want) {
					t.Errorf("Barcodes = %v, want %v", p.Barcodes, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.initial
			has := func(col string) bool {
				return slices.Contains(tt.present, col)
			}
			yes := func(val string) bool {
				return slices.Contains(tt.yesVals, val)
			}

			err := fillImported(&p, tt.values, has, yes)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("fillImported() unexpected error: %v", err)
				}
				if tt.check != nil {
					tt.check(p)
				}
			} else {
				if err == nil {
					t.Fatalf("fillImported() expected error %q, got nil", tt.wantErr)
				}
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != tt.wantErr {
					t.Errorf("fillImported() got error %v, want key %q", err, tt.wantErr)
				}
			}
		})
	}
}

func TestMapValues_Unit(t *testing.T) {
	m := map[int]string{
		0: "zero",
		1: "one",
		2: "two",
	}
	vals := mapValues(m)
	if len(vals) != 3 {
		t.Fatalf("mapValues len = %d, want 3", len(vals))
	}
	for _, expected := range []string{"zero", "one", "two"} {
		if !slices.Contains(vals, expected) {
			t.Errorf("mapValues missing %q", expected)
		}
	}
}

func TestProductFields_Unit(t *testing.T) {
	wantFields := []string{
		"id", "name", "kind", "sku", "section", "parent", "price", "currency",
		"purchase_price", "unit", "vat", "vat_included", "active", "barcodes", "stock", "description",
	}
	if !reflect.DeepEqual(ProductFields, wantFields) {
		t.Errorf("ProductFields = %v, want %v", ProductFields, wantFields)
	}
}
