package bp

import "testing"

// TestCalc: the formulas of «Изменить переменную» — numbers, dates, functions; a wrong one is an error.
func TestCalc(t *testing.T) {
	for expr, want := range map[string]string{
		"1 + 2 * 3":                "7",
		"(1 + 2) * 3":              "9",
		"1,5 * 2":                  "3",
		"0.1 + 0.2":                "0.3",
		"-4 + 10 / 4":              "-1.5",
		"round(2 / 3; 2)":          "0.67",
		"round(2.5)":               "3",
		"max(1, 7, 3) - min(4; 2)": "5",
		"abs(3 - 10)":              "7",
		"2026-11-02 + 3":           "2026-11-05",
		"2026-11-02T15:30 - 1":     "2026-11-01T15:30",
		"2026-11-10 - 2026-11-02":  "8",
		"5000 * 1,2":               "6000",
	} {
		if got, err := calc(expr, expr); err != nil || got != want {
			t.Errorf("%s = %q %v, want %s", expr, got, err, want)
		}
	}
	for _, expr := range []string{"", "1 +", "abc", "2 / 0", "2026-11-02 * 2", "(1", "foo(1)", "1 2", "2026-11-02 + 2026-11-03"} {
		if got, err := calc(expr, expr); err == nil {
			t.Errorf("%q = %q, want an error", expr, got)
		}
	}
}
