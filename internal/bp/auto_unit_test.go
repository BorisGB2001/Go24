package bp

import (
	"context"
	"errors"
	"testing"
)

// TestAutoErrs: helper error handling for worker loops, ignoring context cancellation.
func TestAutoErrs(t *testing.T) {
	if got := errs(nil); got != nil {
		t.Errorf("errs(nil) = %v, want nil", got)
	}
	if got := errs(context.Canceled); got != nil {
		t.Errorf("errs(context.Canceled) = %v, want nil", got)
	}

	testErr := errors.New("database connection failed")
	got := errs(testErr)
	if len(got) != 1 || !errors.Is(got[0], testErr) {
		t.Errorf("errs(testErr) = %v, want [%v]", got, testErr)
	}
}

// TestCardTables: verify mapping of card entity names to CRM database tables.
func TestCardTables(t *testing.T) {
	expected := map[string]string{
		"deal":    "crm_deals",
		"lead":    "crm_leads",
		"contact": "crm_contacts",
		"company": "crm_companies",
	}
	for entity, table := range expected {
		if got := cardTables[entity]; got != table {
			t.Errorf("cardTables[%q] = %q, want %q", entity, got, table)
		}
	}
}
