package crm

import (
	"math"
	"testing"
	"time"
)

func TestInvoiceOverdue_Unit(t *testing.T) {
	yesterday := time.Now().AddDate(0, 0, -1)
	today := time.Now()
	tomorrow := time.Now().AddDate(0, 0, 1)

	tests := []struct {
		name string
		inv  Invoice
		want bool
	}{
		{
			name: "overdue invoice with status new",
			inv:  Invoice{Kind: "invoice", Status: "new", DueDate: &yesterday},
			want: true,
		},
		{
			name: "overdue invoice with status sent",
			inv:  Invoice{Kind: "invoice", Status: "sent", DueDate: &yesterday},
			want: true,
		},
		{
			name: "due today is not overdue",
			inv:  Invoice{Kind: "invoice", Status: "new", DueDate: &today},
			want: false,
		},
		{
			name: "due tomorrow is not overdue",
			inv:  Invoice{Kind: "invoice", Status: "new", DueDate: &tomorrow},
			want: false,
		},
		{
			name: "paid invoice is not overdue",
			inv:  Invoice{Kind: "invoice", Status: "paid", DueDate: &yesterday},
			want: false,
		},
		{
			name: "cancelled invoice is not overdue",
			inv:  Invoice{Kind: "invoice", Status: "cancelled", DueDate: &yesterday},
			want: false,
		},
		{
			name: "quote is not overdue even if past due date",
			inv:  Invoice{Kind: "quote", Status: "new", DueDate: &yesterday},
			want: false,
		},
		{
			name: "nil due date is not overdue",
			inv:  Invoice{Kind: "invoice", Status: "new", DueDate: nil},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.inv.Overdue(); got != tt.want {
				t.Errorf("Invoice.Overdue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInvoiceDue_Unit(t *testing.T) {
	tests := []struct {
		name string
		inv  Invoice
		want float64
	}{
		{
			name: "unpaid invoice",
			inv:  Invoice{Kind: "invoice", Status: "new", Amount: 1000, Paid: 0},
			want: 1000,
		},
		{
			name: "partially paid invoice with cents",
			inv:  Invoice{Kind: "invoice", Status: "sent", Amount: 1000.55, Paid: 400.20},
			want: 600.35,
		},
		{
			name: "fully paid invoice",
			inv:  Invoice{Kind: "invoice", Status: "new", Amount: 500, Paid: 500},
			want: 0,
		},
		{
			name: "overpaid invoice",
			inv:  Invoice{Kind: "invoice", Status: "new", Amount: 500, Paid: 600},
			want: 0,
		},
		{
			name: "status paid",
			inv:  Invoice{Kind: "invoice", Status: "paid", Amount: 1000, Paid: 200},
			want: 0,
		},
		{
			name: "status cancelled",
			inv:  Invoice{Kind: "invoice", Status: "cancelled", Amount: 1000, Paid: 0},
			want: 0,
		},
		{
			name: "quote returns 0 due",
			inv:  Invoice{Kind: "quote", Status: "new", Amount: 1000, Paid: 0},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.inv.Due()
			if math.Abs(got-tt.want) > 1e-6 {
				t.Errorf("Invoice.Due() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInvoiceCardAndPath_Unit(t *testing.T) {
	dealInv := Invoice{DealID: "deal-101", LeadID: ""}
	entity, id := dealInv.Card()
	if entity != "deal" || id != "deal-101" {
		t.Errorf("Card() = (%q, %q), want (\"deal\", \"deal-101\")", entity, id)
	}
	if path := dealInv.CardPath(); path != "/crm/deals/deal-101" {
		t.Errorf("CardPath() = %q, want \"/crm/deals/deal-101\"", path)
	}

	leadInv := Invoice{DealID: "deal-101", LeadID: "lead-202"}
	entityL, idL := leadInv.Card()
	if entityL != "lead" || idL != "lead-202" {
		t.Errorf("Card() = (%q, %q), want (\"lead\", \"lead-202\")", entityL, idL)
	}
	if pathL := leadInv.CardPath(); pathL != "/crm/leads/lead-202" {
		t.Errorf("CardPath() = %q, want \"/crm/leads/lead-202\"", pathL)
	}
}

func TestInvoiceCode_Unit(t *testing.T) {
	inv := Invoice{Number: 42}
	if got := inv.Code(); got != "42" {
		t.Errorf("Invoice.Code() = %q, want \"42\"", got)
	}
}

func TestDateOf_Unit(t *testing.T) {
	if got := dateOf(nil); got != nil {
		t.Errorf("dateOf(nil) = %v, want nil", got)
	}

	tm := time.Date(2026, 10, 28, 15, 30, 0, 0, time.UTC)
	got := dateOf(&tm)
	if got == nil || *got != "2026-10-28" {
		t.Errorf("dateOf(%v) = %v, want \"2026-10-28\"", tm, got)
	}
}
