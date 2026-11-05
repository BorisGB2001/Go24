package bp

import (
	"fmt"
	"testing"
)

// TestStandardDefinitions: generation and validation of standard templates (vacation, trip, invoice, payout).
func TestStandardDefinitions(t *testing.T) {
	fakeTr := func(key string) string {
		return "tr:" + key
	}
	accountant := "acc-123"

	templates := Standard(fakeTr, accountant)
	if len(templates) != len(StdKeys) {
		t.Fatalf("Standard() generated %d templates, want %d", len(templates), len(StdKeys))
	}

	keysSeen := make(map[string]bool)
	for _, tpl := range templates {
		keysSeen[tpl.Std] = true

		if tpl.Entity != "none" {
			t.Errorf("template %s entity = %q, want 'none'", tpl.Std, tpl.Entity)
		}
		if !tpl.Active {
			t.Errorf("template %s should be Active", tpl.Std)
		}
		if tpl.Name != fmt.Sprintf("tr:bp.std.%s", tpl.Std) {
			t.Errorf("template %s name = %q, want 'tr:bp.std.%s'", tpl.Std, tpl.Name, tpl.Std)
		}
		if len(tpl.Steps) == 0 {
			t.Errorf("template %s has no steps", tpl.Std)
		}
		if len(tpl.Fields) == 0 {
			t.Errorf("template %s has no fields", tpl.Std)
		}

		// Verify every standard template passes checkTemplate validation
		copied := tpl
		if err := checkTemplate(&copied, nil); err != nil {
			t.Errorf("standard template %s failed checkTemplate: %v", tpl.Std, err)
		}
	}

	for _, expectedKey := range StdKeys {
		if !keysSeen[expectedKey] {
			t.Errorf("missing standard template key %q", expectedKey)
		}
	}
}
