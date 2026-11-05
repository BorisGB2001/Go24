package bp

import (
	"context"
	"testing"
	"time"
)

// TestCalcExtended: today(), abs, min, max, round, date differences and arithmetic, plus invalid syntax edge cases.
func TestCalcExtended(t *testing.T) {
	todayStr := time.Now().UTC().Format("2006-01-02")
	tomorrowStr := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	yesterdayStr := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	valid := map[string]string{
		"today()":                             todayStr,
		"today() + 1":                         tomorrowStr,
		"today() - 1":                         yesterdayStr,
		"abs(-42)":                            "42",
		"abs(42)":                             "42",
		"abs(0)":                              "0",
		"round(3.14159; 2)":                   "3.14",
		"round(3.14159, 2)":                   "3.14",
		"round(123.456; 0)":                   "123",
		"round(2.5)":                          "3",
		"round(3.5)":                          "4",
		"min(10)":                             "10",
		"min(10; 5; 20)":                      "5",
		"min(10, 5, 20)":                      "5",
		"max(10)":                             "10",
		"max(10; 50; 20)":                     "50",
		"max(10, 50, 20)":                     "50",
		"max(round(1.6), min(5, 3))":          "3",
		"2026-11-02T10:00 + 1":                "2026-11-03T10:00",
		"2026-11-02T10:00 - 1":                "2026-11-01T10:00",
		"2026-11-05T10:00 - 2026-11-02T10:00": "3",
		"10 - (2 + 3) * 2":                    "0",
		"-(-5)":                               "5",
		"2,5 * 4":                             "10",
		"100 / 4 / 5":                         "5",
	}

	for expr, want := range valid {
		got, err := calc(expr, expr)
		if err != nil || got != want {
			t.Errorf("calc(%q) = %q, %v, want %q", expr, got, err, want)
		}
	}

	invalid := []string{
		"today(1)",
		"abs()",
		"abs(1; 2)",
		"min()",
		"max()",
		"round()",
		"round(1; 2; 3)",
		"min(2026-11-02)",
		"max(2026-11-02)",
		"abs(2026-11-02)",
		"round(2026-11-02)",
		"2026-11-02 * 2",
		"2026-11-02 / 2",
		"2026-11-02 + 2026-11-03",
		"5 - 2026-11-02",
		"-2026-11-02",
		"10 / 0",
		"5 / (2 - 2)",
		"(1 + 2",
		"1 + 2)",
		"foo(1)",
		"1.2.3",
		"1,2,3",
		"1 ++ 2",
		"1 +",
		"",
	}

	for _, expr := range invalid {
		if got, err := calc(expr, expr); err == nil {
			t.Errorf("calc(%q) = %q, want error", expr, got)
		}
	}
}

// TestStoreValue: variable filling and formula calculation via Store.value.
func TestStoreValue(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	inst := Instance{
		Entity: "none",
		Values: map[string]string{
			"price": "100",
			"tax":   "0.2",
			"name":  "Alice",
		},
	}

	// Plain text without '=' prefix
	got, err := s.value(ctx, inst, "Hello {name}")
	if err != nil || got != "Hello Alice" {
		t.Errorf("value(plain) = %q, %v, want 'Hello Alice'", got, err)
	}

	// Formula with '=' prefix
	got, err = s.value(ctx, inst, "= {price} * (1 + {tax})")
	if err != nil || got != "120" {
		t.Errorf("value(formula) = %q, %v, want '120'", got, err)
	}

	// Formula error returns err.bp_expr
	_, err = s.value(ctx, inst, "= {price} / 0")
	if errKey(err) != "err.bp_expr" {
		t.Errorf("value(div0) want err.bp_expr, got %v", err)
	}
}
