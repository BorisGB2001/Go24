package tasks

import (
	"strings"
	"testing"
	"time"
)

// TestSortTasks: a click on a header sorts by the column, the next one the other way round; pinned tasks stay on top
// and tasks without the date go to the end either way lists.
func TestSortTasks(t *testing.T) {
	day := func(d int) *time.Time { v := time.Date(2026, 10, d, 9, 0, 0, 0, time.UTC); return &v }
	list := func() []Task {
		return []Task{
			{ProjectKey: "B", Number: 2, Title: "вторая", Deadline: day(5), Status: StatusCompleted, ResponsibleName: "Яна"},
			{ProjectKey: "A", Number: 10, Title: "Альфа", Status: StatusPending, ResponsibleName: "борис"},
			{ProjectKey: "A", Number: 9, Title: "Бета", Deadline: day(3), Status: StatusInProgress, ResponsibleName: "Анна"},
			{ProjectKey: "C", Number: 1, Title: "закреплена", Deadline: day(9), Pinned: true, ResponsibleName: "Вера"},
		}
	}
	keys := func(l []Task) string {
		var out []string
		for _, x := range l {
			out = append(out, x.Key())
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct{ by, want string }{
		{"deadline", "C-1 A-9 B-2 A-10"},
		{"-deadline", "C-1 B-2 A-9 A-10"},
		{"id", "C-1 A-9 A-10 B-2"},
		{"-id", "C-1 B-2 A-10 A-9"},
		{"title", "C-1 A-10 A-9 B-2"},
		{"responsible", "C-1 A-9 A-10 B-2"},
		{"status", "C-1 A-10 A-9 B-2"},
		{"", "B-2 A-10 A-9 C-1"},      // the usual order: left as the store gave it
		{"bogus", "B-2 A-10 A-9 C-1"}, // an unknown sort changes nothing
	} {
		l := list()
		SortTasks(l, c.by)
		if got := keys(l); got != c.want {
			t.Errorf("sort %q = %s, want %s", c.by, got, c.want)
		}
	}
	for _, c := range []struct{ cur, col, want string }{
		{"", "deadline", "deadline"}, {"deadline", "deadline", "-deadline"}, {"-deadline", "deadline", "deadline"},
		{"title", "deadline", "deadline"}, {"", "activity", "-activity"}, {"-activity", "activity", "activity"},
	} {
		if got := NextSort(c.cur, c.col); got != c.want {
			t.Errorf("NextSort(%q, %q) = %q, want %q", c.cur, c.col, got, c.want)
		}
	}
}
