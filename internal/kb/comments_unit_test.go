package kb

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"go24/internal/i18n"
)

func TestCheckComment(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantBody  string
		wantRunes int
		wantErr   string
	}{
		{
			name:      "normal comment",
			body:      "Great document, thanks!",
			wantBody:  "Great document, thanks!",
			wantRunes: len("Great document, thanks!"),
			wantErr:   "",
		},
		{
			name:      "trims whitespace",
			body:      "   Comment with spaces \t\n  ",
			wantBody:  "Comment with spaces",
			wantRunes: len("Comment with spaces"),
			wantErr:   "",
		},
		{
			name:      "empty comment returns error",
			body:      "",
			wantBody:  "",
			wantRunes: 0,
			wantErr:   "err.comment_empty",
		},
		{
			name:      "whitespace only comment returns error",
			body:      "   \t\n\r   ",
			wantBody:  "",
			wantRunes: 0,
			wantErr:   "err.comment_empty",
		},
		{
			name:      "comment at exactly maxComment length",
			body:      strings.Repeat("a", maxComment),
			wantBody:  strings.Repeat("a", maxComment),
			wantRunes: maxComment,
			wantErr:   "",
		},
		{
			name:      "ascii comment exceeding maxComment is truncated",
			body:      strings.Repeat("a", maxComment+10),
			wantBody:  strings.Repeat("a", maxComment),
			wantRunes: maxComment,
			wantErr:   "",
		},
		{
			name:      "cyrillic comment exceeding maxComment truncated by runes",
			body:      strings.Repeat("ю", maxComment+15),
			wantBody:  strings.Repeat("ю", maxComment),
			wantRunes: maxComment,
			wantErr:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkComment(tc.body)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkComment(%s) unexpected error: %v", tc.name, err)
				}
			} else {
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != tc.wantErr {
					t.Fatalf("checkComment(%s) err = %v, want error key %q", tc.name, err, tc.wantErr)
				}
			}
			if got != tc.wantBody {
				t.Fatalf("checkComment(%s) got len %d, want len %d", tc.name, len(got), len(tc.wantBody))
			}
			if utf8.RuneCountInString(got) != tc.wantRunes {
				t.Fatalf("checkComment(%s) got %d runes, want %d", tc.name, utf8.RuneCountInString(got), tc.wantRunes)
			}
		})
	}
}

func TestDeref(t *testing.T) {
	if got := deref(nil); got != "" {
		t.Fatalf("deref(nil) = %q, want \"\"", got)
	}

	s := "example"
	if got := deref(&s); got != "example" {
		t.Fatalf("deref(&s) = %q, want %q", got, "example")
	}

	empty := ""
	if got := deref(&empty); got != "" {
		t.Fatalf("deref(&empty) = %q, want \"\"", got)
	}
}
