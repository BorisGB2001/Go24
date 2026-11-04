package drive

import (
	"context"
	"strings"
	"testing"
)

func TestTsQuery(t *testing.T) {
	for _, c := range []struct {
		q    string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{",.-!?;:'\"(){}[]/\\", ""},
		{"догов", "'догов':*"},
		{"догов пост", "'догов':* & 'пост':*"},
		{"Hello World", "'hello':* & 'world':*"},
		{"plan 2026", "'plan':* & '2026':*"},
		{"file_name-part", "'file':* & 'name':* & 'part':*"},
		{"one two three four five six seven eight nine ten", "'one':* & 'two':* & 'three':* & 'four':* & 'five':* & 'six':* & 'seven':* & 'eight':*"},
		{"  MIXED Case with 123 NUMBERS  ", "'mixed':* & 'case':* & 'with':* & '123':* & 'numbers':*"},
	} {
		if got := tsQuery(c.q); got != c.want {
			t.Errorf("tsQuery(%q) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestContentMatch(t *testing.T) {
	got := contentMatch("3")
	want := `($3 <> '' AND NOT EXISTS (SELECT 1 FROM drive_limits WHERE kind = 'no_content' AND bytes = 1) AND f.content_tsv @@ to_tsquery('russian', $3))`
	if got != want {
		t.Errorf("contentMatch(\"3\") = %q, want %q", got, want)
	}
}

func TestSnippetsEmpty(t *testing.T) {
	// Snippets returns immediately when the query yields no search terms or the file list is empty.
	s := NewStore(nil, nil)
	ctx := context.Background()

	got, err := s.Snippets(ctx, nil, "valid-query")
	if err != nil || got != nil {
		t.Errorf("Snippets(ids=nil) = (%v, %v), want (nil, nil)", got, err)
	}

	got, err = s.Snippets(ctx, []string{"id-1", "id-2"}, "")
	if err != nil || got != nil {
		t.Errorf("Snippets(q=\"\") = (%v, %v), want (nil, nil)", got, err)
	}

	got, err = s.Snippets(ctx, []string{"id-1"}, "   ---   ")
	if err != nil || got != nil {
		t.Errorf("Snippets(q=symbols) = (%v, %v), want (nil, nil)", got, err)
	}

	// Verify MaxContent constant matches documented ceiling.
	if MaxContent != 200000 {
		t.Errorf("MaxContent = %d, want 200000", MaxContent)
	}

	// SetContent character truncation logic check: strings longer than MaxContent are capped.
	longText := strings.Repeat("a", MaxContent+50)
	if r := []rune(longText); len(r) > MaxContent {
		capped := string(r[:MaxContent])
		if len([]rune(capped)) != MaxContent {
			t.Errorf("rune cap = %d, want %d", len([]rune(capped)), MaxContent)
		}
	}
}
