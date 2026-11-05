package tasks

import (
	"slices"
	"testing"
	"time"
)

func TestImportColumnsMapping(t *testing.T) {
	expected := map[string]string{
		"title":         "title",
		"название":      "title",
		"задача":        "title",
		"name":          "title",
		"description":   "description",
		"описание":      "description",
		"responsible":   "responsible",
		"ответственный": "responsible",
		"исполнитель":   "responsible",
		"assignee":      "responsible",
		"accomplices":   "accomplices",
		"соисполнители": "accomplices",
		"соисполнитель": "accomplices",
		"auditors":      "auditors",
		"наблюдатели":   "auditors",
		"наблюдатель":   "auditors",
		"deadline":      "deadline",
		"крайний срок":  "deadline",
		"срок":          "deadline",
		"priority":      "priority",
		"важная":        "priority",
		"важность":      "priority",
		"приоритет":     "priority",
		"important":     "priority",
		"tags":          "tags",
		"теги":          "tags",
		"тег":           "tags",
		"project":       "project",
		"проект":        "project",
		"группа":        "project",
		"group":         "project",
		"checklist":     "checklist",
		"чек-лист":      "checklist",
		"чеклист":       "checklist",
	}

	for k, want := range expected {
		if got := ImportColumns[k]; got != want {
			t.Errorf("ImportColumns[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestHighWords(t *testing.T) {
	positives := []string{
		"да", "yes", "1", "+", "true", "высокий", "высокая", "high", "важная", "важно", "срочно",
	}
	for _, w := range positives {
		if !highWords[w] {
			t.Errorf("highWords[%q] must be true", w)
		}
	}

	negatives := []string{
		"нет", "no", "0", "-", "false", "низкий", "обычный", "normal", "low", "",
	}
	for _, w := range negatives {
		if highWords[w] {
			t.Errorf("highWords[%q] must be false", w)
		}
	}
}

func TestParseImportDate_AllLayouts(t *testing.T) {
	tests := []struct {
		in      string
		endH    int
		endM    int
		want    string // in local time format YYYY-MM-DD HH:MM:SS
		wantErr bool
	}{
		// DateTime layouts
		{"02.01.2006 15:04", 18, 0, "2006-01-02 15:04:00", false},
		{"02.01.2006 15:04:05", 18, 0, "2006-01-02 15:04:05", false},
		{"2006-01-02 15:04", 18, 0, "2006-01-02 15:04:00", false},
		{"2006-01-02T15:04", 18, 0, "2006-01-02 15:04:00", false},
		{"2006-01-02 15:04:05", 18, 0, "2006-01-02 15:04:05", false},
		{"2.1.2006 15:04", 18, 0, "2006-01-02 15:04:00", false},

		// Date-only layouts (take endH, endM)
		{"15.10.2026", 18, 30, "2026-10-15 18:30:00", false},
		{"2026-10-15", 17, 0, "2026-10-15 17:00:00", false},
		{"5.9.2026", 19, 0, "2026-09-05 19:00:00", false},
		{"15.10.26", 18, 0, "2026-10-15 18:00:00", false},

		// Trimmed input
		{"  2026-10-15 10:00  ", 18, 0, "2026-10-15 10:00:00", false},

		// Invalid inputs
		{"", 18, 0, "", true},
		{"invalid date", 18, 0, "", true},
		{"32.01.2026", 18, 0, "", true},
		{"2026-02-30", 18, 0, "", true},
		{"2026-13-01", 18, 0, "", true},
		{"yesterday", 18, 0, "", true},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseImportDate(tc.in, tc.endH, tc.endM)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseImportDate(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr {
				s := got.Format("2006-01-02 15:04:05")
				if s != tc.want {
					t.Errorf("ParseImportDate(%q) = %q, want %q", tc.in, s, tc.want)
				}
			}
		})
	}
}

func TestSplitList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty string", "", nil},
		{"single item", "Alice", []string{"Alice"}},
		{"comma separated", "Alice, Bob, Charlie", []string{"Alice", "Bob", "Charlie"}},
		{"semicolon separated", "Alice; Bob; Charlie", []string{"Alice", "Bob", "Charlie"}},
		{"mixed separators with blanks", " ; Alice , ; Bob ;; Charlie , ", []string{"Alice", "Bob", "Charlie"}},
		{"whitespaces only", "   ; ,   ", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitList(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("splitList(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNameKey(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  Иван   Иванов  ", "иван иванов"},
		{"Алёна   Фёдорова", "алена федорова"},
		{"JOHN   DOE", "john doe"},
		{"\tАнна\nЛи\t", "анна ли"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := nameKey(tc.in); got != tc.want {
				t.Errorf("nameKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExportName(t *testing.T) {
	date := time.Date(2026, 10, 28, 15, 30, 0, 0, time.UTC)
	want := "tasks-2026-10-28.csv"
	if got := ExportName(date); got != want {
		t.Errorf("ExportName() = %q, want %q", got, want)
	}
}
