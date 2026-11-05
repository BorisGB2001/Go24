package tasks

import (
	"slices"
	"testing"
)

func TestFold(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"Hello World", "hello world"},
		{"ПЁТР ФЁДОРОВ", "петр федоров"},
		{"ёжик", "ежик"},
		{"ЕЖИК", "ежик"},
	}

	for _, tc := range tests {
		if got := fold(tc.in); got != tc.want {
			t.Errorf("fold(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTokens(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"John Doe", []string{"john", "doe"}},
		{"Иван Иванов (Отдел продаж) ivan@example.com", []string{"иван", "иванов", "отдел", "продаж", "ivan", "example", "com"}},
		{"  one,,two...three!! ", []string{"one", "two", "three"}},
	}

	for _, tc := range tests {
		got := tokens(tc.in)
		if !slices.Equal(got, tc.want) {
			t.Errorf("tokens(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPrefixes(t *testing.T) {
	toks := []string{"иван", "иванов", "бухгалтер"}

	tests := []struct {
		name  string
		words []string
		want  bool
	}{
		{"single prefix match", []string{"ив"}, true},
		{"multiple prefixes match", []string{"ив", "бух"}, true},
		{"all words match", []string{"иван", "иванов", "бухгалтер"}, true},
		{"one word does not match", []string{"ив", "юрист"}, false},
		{"empty words", nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := prefixes(tc.words, toks); got != tc.want {
				t.Errorf("prefixes(%v, %v) = %v, want %v", tc.words, toks, got, tc.want)
			}
		})
	}
}

func TestSwitchLayout_Bidirectional(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		// QWERTY -> ЙЦУКЕН
		{"ghbdtn", "привет"},
		{"ntcn", "тест"},
		{"bdfy", "иван"},
		// ЙЦУКЕН -> QWERTY
		{"руддщ", "hello"},
		{"еуые", "test"},
		{"ищкшы", "boris"},
		// Non-alphabet characters preserved (digits, exclamation, spaces)
		{"ghbdtn 123!", "привет 123!"},
		// Punctuation keys mapping according to keyboard layout
		{"?", "?"},         // not in layout string
		{"bdfy,", "иванб"}, // comma is on the 'б' key
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := SwitchLayout(tc.in); got != tc.want {
				t.Errorf("SwitchLayout(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMatchPeople_RankingAndLayout(t *testing.T) {
	people := []PersonHit{
		{ID: "1", Name: "Иван Иванов", Position: "Бухгалтер", Department: "Финансы", Email: "ivan@example.com"},
		{ID: "2", Name: "Петр Петров", Position: "Инженер", Department: "Разработка", Email: "petr@example.com", recent: 1},
		{ID: "3", Name: "Анна Иванова", Position: "Директор", Department: "Руководство", Email: "anna@example.com", recent: 2},
	}

	ids := func(list []PersonHit) []string {
		var out []string
		for _, p := range list {
			out = append(out, p.ID)
		}
		return out
	}

	// Empty query with recent=true returns only recent people ordered by recent rank
	recentList := MatchPeople(people, "", true)
	if got := ids(recentList); !slices.Equal(got, []string{"2", "3"}) {
		t.Errorf("MatchPeople empty with recent = %v, want [2 3]", got)
	}

	// Match by name in wrong layout: "bdfy" -> "иван" (Иван Иванов, Анна Иванова)
	layoutList := MatchPeople(people, "bdfy", false)
	if len(layoutList) != 2 || layoutList[0].ID != "1" || layoutList[1].ID != "3" {
		t.Errorf("MatchPeople layout switch = %v, want [1 3]", ids(layoutList))
	}

	// Match by position: "бух" -> Иван Иванов
	posList := MatchPeople(people, "бух", false)
	if len(posList) != 1 || posList[0].ID != "1" {
		t.Errorf("MatchPeople by position = %v, want [1]", ids(posList))
	}

	// Match by email domain: "example" matches all
	allList := MatchPeople(people, "example", false)
	if len(allList) != 3 {
		t.Errorf("MatchPeople by email = %v, want 3 results", ids(allList))
	}
}
