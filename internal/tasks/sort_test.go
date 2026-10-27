package tasks

import (
	"fmt"
	"slices"
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

func TestPaginate(t *testing.T) {
	list := make([]Task, 23)
	for i := range list {
		list[i].Number = i + 1
	}
	for _, c := range []struct{ n, size, first, len, page, pages int }{
		{1, 10, 1, 10, 1, 3}, {3, 10, 21, 3, 3, 3}, {9, 10, 21, 3, 3, 3}, {0, 10, 1, 10, 1, 3}, {2, 50, 1, 23, 1, 1}, {1, 0, 1, 23, 1, 1},
	} {
		got, page, pages := Paginate(list, c.n, c.size)
		if len(got) != c.len || got[0].Number != c.first || page != c.page || pages != c.pages {
			t.Errorf("page %d of %d: %d tasks from %d, page %d of %d", c.n, c.size, len(got), got[0].Number, page, pages)
		}
	}
	if got, page, pages := Paginate([]Task(nil), 2, 10); len(got) != 0 || page != 1 || pages != 1 {
		t.Errorf("an empty list: %d %d %d", len(got), page, pages)
	}
}

func TestTree(t *testing.T) {
	mk := func(n, parent int) Task {
		t := Task{ProjectKey: "T", Number: n}
		if parent > 0 {
			t.ParentKey = fmt.Sprintf("T-%d", parent)
		}
		return t
	}
	// 1 ← 3 ← 4; 2; 5 has its parent 9 outside the list; 6 ↔ 7 point at each other
	groups := Tree([]Task{mk(4, 3), mk(1, 0), mk(2, 0), mk(3, 1), mk(5, 9), mk(6, 7), mk(7, 6)})
	var got []string
	for _, g := range groups {
		var s []string
		for _, r := range g {
			s = append(s, fmt.Sprintf("%d:%d:%s:%d", r.Number, r.Depth, strings.Join(r.Path, ","), r.Kids))
		}
		got = append(got, strings.Join(s, " "))
	}
	want := []string{"1:0::1 3:1:T-1:1 4:2:T-1,T-3:0", "2:0::0", "5:0::0", "6:0::1 7:1:T-6:0"}
	if !slices.Equal(got, want) {
		t.Errorf("Tree = %q, want %q", got, want)
	}
}
