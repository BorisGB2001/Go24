package tasks

import (
	"slices"
	"strings"
	"testing"
)

func TestParseKeywords(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "empty string",
			in:   "",
			want: []string{},
		},
		{
			name: "single keyword",
			in:   "Разработка",
			want: []string{"Разработка"},
		},
		{
			name: "strip hash prefix",
			in:   "#разработка, #golang",
			want: []string{"разработка", "golang"},
		},
		{
			name: "delimiters: comma, semicolon, newline",
			in:   "Разработка, Golang; Backend\nAPI",
			want: []string{"Разработка", "Golang", "Backend", "API"},
		},
		{
			name: "normalize inner whitespace",
			in:   "  Web   development  ,   Mobile   apps  ",
			want: []string{"Web development", "Mobile apps"},
		},
		{
			name: "case-insensitive deduplication preserves first case",
			in:   "Golang, GOLANG, golang, GoLang, Rust",
			want: []string{"Golang", "Rust"},
		},
		{
			name: "keyword truncated to maxKeywordLen (50 runes)",
			in:   strings.Repeat("ж", 60),
			want: []string{strings.Repeat("ж", 50)},
		},
		{
			name: "capped at maxKeywords (20)",
			in: func() string {
				var words []string
				for i := 1; i <= 30; i++ {
					words = append(words, "word"+string(rune('a'+i)))
				}
				return strings.Join(words, ", ")
			}(),
			want: func() []string {
				var words []string
				for i := 1; i <= 20; i++ {
					words = append(words, "word"+string(rune('a'+i)))
				}
				return words
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseKeywords(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ParseKeywords(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
