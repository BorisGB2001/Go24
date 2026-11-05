package kb

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestCheckPage(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		body      string
		wantTitle string
		wantErr   string // i18n error key, empty if no error expected
	}{
		{
			name:      "valid normal page",
			title:     "Page Title",
			body:      "Page Body content",
			wantTitle: "Page Title",
			wantErr:   "",
		},
		{
			name:      "whitespace trimmed from title",
			title:     "   Spaced Title \t\n  ",
			body:      "body",
			wantTitle: "Spaced Title",
			wantErr:   "",
		},
		{
			name:      "empty title returns error",
			title:     "",
			body:      "body",
			wantTitle: "",
			wantErr:   "err.kb_page",
		},
		{
			name:      "whitespace only title returns error",
			title:     "   \t\n   ",
			body:      "body",
			wantTitle: "",
			wantErr:   "err.kb_page",
		},
		{
			name:      "title exactly 300 runes is valid",
			title:     strings.Repeat("a", 300),
			body:      "body",
			wantTitle: strings.Repeat("a", 300),
			wantErr:   "",
		},
		{
			name:      "title 301 runes returns error",
			title:     strings.Repeat("a", 301),
			body:      "body",
			wantTitle: "",
			wantErr:   "err.kb_page",
		},
		{
			name:      "cyrillic title exactly 300 runes is valid",
			title:     strings.Repeat("ж", 300),
			body:      "body",
			wantTitle: strings.Repeat("ж", 300),
			wantErr:   "",
		},
		{
			name:      "cyrillic title 301 runes returns error",
			title:     strings.Repeat("ж", 301),
			body:      "body",
			wantTitle: "",
			wantErr:   "err.kb_page",
		},
		{
			name:      "body exactly 200000 bytes is valid",
			title:     "Title",
			body:      strings.Repeat("b", 200000),
			wantTitle: "Title",
			wantErr:   "",
		},
		{
			name:      "body 200001 bytes returns error",
			title:     "Title",
			body:      strings.Repeat("b", 200001),
			wantTitle: "",
			wantErr:   "err.kb_page",
		},
		{
			name:      "empty body is valid",
			title:     "Title",
			body:      "",
			wantTitle: "Title",
			wantErr:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotTitle, err := checkPage(tc.title, tc.body)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkPage(%q, ...) unexpected error: %v", tc.title, err)
				}
			} else {
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != tc.wantErr {
					t.Fatalf("checkPage(%q, ...) err = %v, want error key %q", tc.title, err, tc.wantErr)
				}
			}
			if gotTitle != tc.wantTitle {
				t.Fatalf("checkPage(%q, ...) title = %q, want %q", tc.title, gotTitle, tc.wantTitle)
			}
		})
	}
}
