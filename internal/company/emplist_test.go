package company

import (
	"slices"
	"testing"
	"time"
)

// TestSortEmployees: by a column with the name breaking ties, backwards, and the empty values last either way.
func TestSortEmployees(t *testing.T) {
	day := func(s string) *time.Time { d, _ := time.Parse(time.DateOnly, s); return &d }
	list := []Profile{
		{Name: "Вера", Position: "Юрист", Birthday: day("1990-03-01"), Departments: []DeptRef{{Name: "Право"}}},
		{Name: "анна", Position: "Аналитик", Birthday: day("1985-12-31")},
		{Name: "Борис", Birthday: day("2000-01-15"), Employment: day("2020-05-01"), Departments: []DeptRef{{Name: "Бухгалтерия"}}},
		{Name: "Глеб", Position: "Аналитик", Employment: day("2024-01-01")},
	}
	names := func() []string {
		var out []string
		for _, p := range list {
			out = append(out, p.Name)
		}
		return out
	}
	for _, c := range []struct {
		by   string
		desc bool
		want []string
	}{
		{"", false, []string{"анна", "Борис", "Вера", "Глеб"}},
		{"name", true, []string{"Глеб", "Вера", "Борис", "анна"}},
		{"position", false, []string{"анна", "Глеб", "Вера", "Борис"}},
		{"position", true, []string{"Вера", "анна", "Глеб", "Борис"}},
		{"department", false, []string{"Борис", "Вера", "анна", "Глеб"}},
		{"birthday", false, []string{"Борис", "Вера", "анна", "Глеб"}},
		{"employment", true, []string{"Глеб", "Борис", "анна", "Вера"}},
		{"last_login", false, []string{"анна", "Борис", "Вера", "Глеб"}},
	} {
		SortEmployees(list, c.by, c.desc)
		if got := names(); !slices.Equal(got, c.want) {
			t.Errorf("%s desc=%v: %v, want %v", c.by, c.desc, got, c.want)
		}
	}
	if got := knownColumns([]string{"email", "x", "position", "email"}); !slices.Equal(got, []string{"position", "email"}) {
		t.Errorf("known columns: %v", got)
	}
}
