package tasks

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidSort(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"id", true},
		{"-id", true},
		{"title", true},
		{"-title", true},
		{"status", true},
		{"-status", true},
		{"activity", true},
		{"-activity", true},
		{"deadline", true},
		{"-deadline", true},
		{"creator", true},
		{"-creator", true},
		{"responsible", true},
		{"-responsible", true},
		{"project", true},
		{"-project", true},
		{"priority", true},
		{"-priority", true},
		{"created", true},
		{"-created", true},
		{"closed", true},
		{"-closed", true},
		{"plan", true},
		{"-plan", true},
		{"unknown", false},
		{"-unknown", false},
		{"--id", false},
		{"id-", false},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := validSort(tc.in); got != tc.want {
				t.Errorf("validSort(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNextSort_AllBranches(t *testing.T) {
	tests := []struct {
		cur  string
		col  string
		want string
	}{
		// Columns with sortDescFirst (activity, created, closed, priority)
		{"", "activity", "-activity"},
		{"-activity", "activity", "activity"},
		{"activity", "activity", "-activity"},
		{"other", "activity", "-activity"},
		{"", "created", "-created"},
		{"-created", "created", "created"},
		{"", "closed", "-closed"},
		{"", "priority", "-priority"},

		// Columns without sortDescFirst (id, title, status, deadline, creator, responsible, project, plan)
		{"", "title", "title"},
		{"title", "title", "-title"},
		{"-title", "title", "title"},
		{"other", "title", "title"},
		{"", "id", "id"},
		{"id", "id", "-id"},
		{"-id", "id", "id"},
		{"", "status", "status"},
		{"", "deadline", "deadline"},
		{"deadline", "deadline", "-deadline"},
		{"-deadline", "deadline", "deadline"},
		{"", "plan", "plan"},
		{"plan", "plan", "-plan"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_%s", tc.cur, tc.col), func(t *testing.T) {
			if got := NextSort(tc.cur, tc.col); got != tc.want {
				t.Errorf("NextSort(%q, %q) = %q, want %q", tc.cur, tc.col, got, tc.want)
			}
		})
	}
}

func TestSortTasks_AllColumnsAndNils(t *testing.T) {
	tm := func(day int) time.Time {
		return time.Date(2026, 10, day, 10, 0, 0, 0, time.UTC)
	}
	pTm := func(day int) *time.Time {
		t := tm(day)
		return &t
	}

	tasks := func() []Task {
		return []Task{
			{
				ProjectKey:      "PRJ",
				Number:          3,
				Title:           "Charlie",
				Status:          StatusCompleted,
				Priority:        Priority(0),
				CreatorName:     "Zara",
				ResponsibleName: "Alice",
				CreatedAt:       tm(1),
				UpdatedAt:       tm(5),
				Deadline:        pTm(10),
				ClosedAt:        pTm(20),
				StartPlan:       pTm(2),
			},
			{
				ProjectKey:      "PRJ",
				Number:          1,
				Title:           "alpha",
				Status:          StatusPending,
				Priority:        Priority(2),
				CreatorName:     "Bob",
				ResponsibleName: "Charlie",
				CreatedAt:       tm(3),
				UpdatedAt:       tm(4),
				Deadline:        nil, // no deadline
				ClosedAt:        nil,
				StartPlan:       nil,
			},
			{
				ProjectKey:      "PRJ",
				Number:          2,
				Title:           "Bravo",
				Status:          StatusInProgress,
				Priority:        Priority(1),
				CreatorName:     "Alice",
				ResponsibleName: "Bob",
				CreatedAt:       tm(2),
				UpdatedAt:       tm(6),
				Deadline:        pTm(5),
				ClosedAt:        pTm(15),
				StartPlan:       pTm(1),
			},
		}
	}

	nums := func(list []Task) []int {
		var out []int
		for _, x := range list {
			out = append(out, x.Number)
		}
		return out
	}

	tests := []struct {
		by   string
		want []int
	}{
		// ID
		{"id", []int{1, 2, 3}},
		{"-id", []int{3, 2, 1}},
		// Title (case-insensitive)
		{"title", []int{1, 2, 3}}, // alpha, Bravo, Charlie
		{"-title", []int{3, 2, 1}},
		// Status order: Pending(0) < InProgress(1) < Completed(4)
		{"status", []int{1, 2, 3}},
		{"-status", []int{3, 2, 1}},
		// Activity (UpdatedAt)
		{"activity", []int{1, 3, 2}}, // 4, 5, 6
		{"-activity", []int{2, 3, 1}},
		// Priority (0 low, 1 normal, 2 high)
		{"priority", []int{3, 2, 1}}, // 0, 1, 2
		{"-priority", []int{1, 2, 3}},
		// Creator (Alice < Bob < Zara)
		{"creator", []int{2, 1, 3}},
		{"-creator", []int{3, 1, 2}},
		// Responsible (Alice < Bob < Charlie)
		{"responsible", []int{3, 2, 1}},
		{"-responsible", []int{1, 2, 3}},
		// CreatedAt
		{"created", []int{3, 2, 1}}, // 1, 2, 3
		{"-created", []int{1, 2, 3}},
		// Dates with nil: nils must go to the end either way!
		{"deadline", []int{2, 3, 1}},  // 5, 10, nil
		{"-deadline", []int{3, 2, 1}}, // 10, 5, nil
		{"closed", []int{2, 3, 1}},    // 15, 20, nil
		{"-closed", []int{3, 2, 1}},   // 20, 15, nil
		{"plan", []int{2, 3, 1}},      // 1, 2, nil
		{"-plan", []int{3, 2, 1}},     // 2, 1, nil
	}

	for _, tc := range tests {
		t.Run(tc.by, func(t *testing.T) {
			l := tasks()
			SortTasks(l, tc.by)
			got := nums(l)
			if !slices.Equal(got, tc.want) {
				t.Errorf("SortTasks(by=%q) = %v, want %v", tc.by, got, tc.want)
			}
		})
	}

	// Project comparison with different keys
	diffProj := []Task{
		{ProjectKey: "ZZ", Number: 1},
		{ProjectKey: "aa", Number: 2},
	}
	SortTasks(diffProj, "project")
	if diffProj[0].ProjectKey != "aa" {
		t.Errorf("expected aa before ZZ in case-insensitive sort, got %v", diffProj[0].ProjectKey)
	}

	// Empty and invalid sort changes nothing
	emptySortList := tasks()
	SortTasks(emptySortList, "")
	if !slices.Equal(nums(emptySortList), []int{3, 1, 2}) {
		t.Error("empty sort must not change order")
	}

	badSortList := tasks()
	SortTasks(badSortList, "nonexistent")
	if !slices.Equal(nums(badSortList), []int{3, 1, 2}) {
		t.Error("bad sort must not change order")
	}
}

func TestPaginate_Detailed(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}

	// Negative size defaults to DefaultPageSize (50)
	got, page, pages := Paginate(items, 1, -5)
	if len(got) != 5 || page != 1 || pages != 1 {
		t.Errorf("negative size: len=%d, page=%d, pages=%d", len(got), page, pages)
	}

	// Page < 1 clamps to 1
	got, page, pages = Paginate(items, -1, 2)
	if len(got) != 2 || got[0] != "a" || page != 1 || pages != 3 {
		t.Errorf("page < 1: got=%v, page=%d, pages=%d", got, page, pages)
	}

	// Page > total pages clamps to last page
	got, page, pages = Paginate(items, 10, 2)
	if len(got) != 1 || got[0] != "e" || page != 3 || pages != 3 {
		t.Errorf("page > pages: got=%v, page=%d, pages=%d", got, page, pages)
	}
}

func TestTree_CyclesAndLeaves(t *testing.T) {
	// Empty list
	if got := Tree(nil); len(got) != 0 {
		t.Errorf("Tree(nil) = %v, want empty", got)
	}

	// Independent tasks (no parents)
	flat := []Task{
		{ProjectKey: "A", Number: 1},
		{ProjectKey: "A", Number: 2},
	}
	gotFlat := Tree(flat)
	if len(gotFlat) != 2 || gotFlat[0][0].Number != 1 || gotFlat[1][0].Number != 2 {
		t.Errorf("Tree(flat) = %+v", gotFlat)
	}

	// Task that references itself as parent: t.ParentKey == t.Key()
	selfParent := []Task{
		{ProjectKey: "A", Number: 1, ParentKey: "A-1"},
	}
	gotSelf := Tree(selfParent)
	if len(gotSelf) != 1 || gotSelf[0][0].Depth != 0 {
		t.Errorf("self-parented task must be top-level: %+v", gotSelf)
	}

	// Deep hierarchy A-1 -> A-2 -> A-3
	deep := []Task{
		{ProjectKey: "A", Number: 1},
		{ProjectKey: "A", Number: 2, ParentKey: "A-1"},
		{ProjectKey: "A", Number: 3, ParentKey: "A-2"},
	}
	gotDeep := Tree(deep)
	if len(gotDeep) != 1 || len(gotDeep[0]) != 3 {
		t.Fatalf("deep tree group length = %d", len(gotDeep[0]))
	}
	r1, r2, r3 := gotDeep[0][0], gotDeep[0][1], gotDeep[0][2]
	if r1.Depth != 0 || r1.Kids != 1 {
		t.Errorf("r1: depth=%d, kids=%d", r1.Depth, r1.Kids)
	}
	if r2.Depth != 1 || r2.Kids != 1 || strings.Join(r2.Path, ",") != "A-1" {
		t.Errorf("r2: depth=%d, kids=%d, path=%v", r2.Depth, r2.Kids, r2.Path)
	}
	if r3.Depth != 2 || r3.Kids != 0 || strings.Join(r3.Path, ",") != "A-1,A-2" {
		t.Errorf("r3: depth=%d, kids=%d, path=%v", r3.Depth, r3.Kids, r3.Path)
	}
}
