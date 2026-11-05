package tasks

import (
	"slices"
	"testing"
)

func TestChecklistDone(t *testing.T) {
	tests := []struct {
		name  string
		items []ChecklistItem
		want  int
	}{
		{name: "empty items", items: nil, want: 0},
		{
			name: "no items done",
			items: []ChecklistItem{
				{Title: "one", Done: false},
				{Title: "two", Done: false},
			},
			want: 0,
		},
		{
			name: "all items done",
			items: []ChecklistItem{
				{Title: "one", Done: true},
				{Title: "two", Done: true},
				{Title: "three", Done: true},
			},
			want: 3,
		},
		{
			name: "mixed items done",
			items: []ChecklistItem{
				{Title: "one", Done: true},
				{Title: "two", Done: false},
				{Title: "three", Done: true},
				{Title: "four", Done: false},
			},
			want: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cl := Checklist{Items: tc.items}
			if got := cl.Done(); got != tc.want {
				t.Errorf("Checklist.Done() = %d, want %d", got, tc.want)
			}
			card := TaskCard{Checklist: tc.items}
			if got := card.ChecklistDone(); got != tc.want {
				t.Errorf("TaskCard.ChecklistDone() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTaskCardChecklistLines(t *testing.T) {
	tests := []struct {
		name       string
		checklists []Checklist
		want       []string
	}{
		{
			name:       "empty checklists",
			checklists: nil,
			want:       nil,
		},
		{
			name: "single checklist without title",
			checklists: []Checklist{
				{
					Title: "",
					Items: []ChecklistItem{
						{Title: "Buy milk"},
						{Title: "Buy bread"},
					},
				},
			},
			want: []string{"Buy milk", "Buy bread"},
		},
		{
			name: "single checklist with title",
			checklists: []Checklist{
				{
					Title: "Groceries",
					Items: []ChecklistItem{
						{Title: "Buy milk"},
						{Title: "Buy bread"},
					},
				},
			},
			want: []string{"# Groceries", "Buy milk", "Buy bread"},
		},
		{
			name: "multiple checklists first without title",
			checklists: []Checklist{
				{
					Title: "",
					Items: []ChecklistItem{
						{Title: "Step 1"},
					},
				},
				{
					Title: "Phase 2",
					Items: []ChecklistItem{
						{Title: "Step 2"},
						{Title: "Step 3"},
					},
				},
				{
					Title: "",
					Items: []ChecklistItem{
						{Title: "Final step"},
					},
				},
			},
			want: []string{
				"Step 1",
				"# Phase 2",
				"Step 2",
				"Step 3",
				"# ",
				"Final step",
			},
		},
		{
			name: "checklist with no items but title",
			checklists: []Checklist{
				{
					Title: "Empty list",
					Items: nil,
				},
			},
			want: []string{"# Empty list"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			card := TaskCard{Checklists: tc.checklists}
			got := card.ChecklistLines()
			if !slices.Equal(got, tc.want) {
				t.Errorf("ChecklistLines() = %q, want %q", got, tc.want)
			}
		})
	}
}
