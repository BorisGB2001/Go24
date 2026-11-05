package kb

import (
	"reflect"
	"strings"
	"testing"
)

func TestDiff_Table(t *testing.T) {
	tests := []struct {
		name     string
		old, cur string
		want     []DiffLine
	}{
		{
			name: "both empty",
			old:  "",
			cur:  "",
			want: []DiffLine{{"=", ""}},
		},
		{
			name: "old empty cur non-empty",
			old:  "",
			cur:  "alpha",
			want: []DiffLine{{"-", ""}, {"+", "alpha"}},
		},
		{
			name: "old non-empty cur empty",
			old:  "alpha",
			cur:  "",
			want: []DiffLine{{"-", "alpha"}, {"+", ""}},
		},
		{
			name: "identical single line",
			old:  "same line",
			cur:  "same line",
			want: []DiffLine{{"=", "same line"}},
		},
		{
			name: "identical multi line",
			old:  "line 1\nline 2\nline 3",
			cur:  "line 1\nline 2\nline 3",
			want: []DiffLine{{"=", "line 1"}, {"=", "line 2"}, {"=", "line 3"}},
		},
		{
			name: "crlf normalization matches lf",
			old:  "line 1\r\nline 2\r\nline 3",
			cur:  "line 1\nline 2\nline 3",
			want: []DiffLine{{"=", "line 1"}, {"=", "line 2"}, {"=", "line 3"}},
		},
		{
			name: "disjoint texts",
			old:  "a\nb",
			cur:  "c\nd",
			want: []DiffLine{{"-", "a"}, {"-", "b"}, {"+", "c"}, {"+", "d"}},
		},
		{
			name: "addition at head",
			old:  "b\nc",
			cur:  "a\nb\nc",
			want: []DiffLine{{"+", "a"}, {"=", "b"}, {"=", "c"}},
		},
		{
			name: "addition at tail",
			old:  "a\nb",
			cur:  "a\nb\nc",
			want: []DiffLine{{"=", "a"}, {"=", "b"}, {"+", "c"}},
		},
		{
			name: "addition in middle",
			old:  "a\nc",
			cur:  "a\nb\nc",
			want: []DiffLine{{"=", "a"}, {"+", "b"}, {"=", "c"}},
		},
		{
			name: "deletion at head",
			old:  "a\nb\nc",
			cur:  "b\nc",
			want: []DiffLine{{"-", "a"}, {"=", "b"}, {"=", "c"}},
		},
		{
			name: "deletion at tail",
			old:  "a\nb\nc",
			cur:  "a\nb",
			want: []DiffLine{{"=", "a"}, {"=", "b"}, {"-", "c"}},
		},
		{
			name: "deletion in middle",
			old:  "a\nb\nc",
			cur:  "a\nc",
			want: []DiffLine{{"=", "a"}, {"-", "b"}, {"=", "c"}},
		},
		{
			name: "replace in middle",
			old:  "a\nb\nc",
			cur:  "a\nx\nc",
			want: []DiffLine{{"=", "a"}, {"-", "b"}, {"+", "x"}, {"=", "c"}},
		},
		{
			name: "duplicate lines with shifts",
			old:  "a\na\nb",
			cur:  "a\nb\nb",
			want: []DiffLine{{"=", "a"}, {"-", "a"}, {"=", "b"}, {"+", "b"}},
		},
		{
			name: "cyrillic text diff",
			old:  "Пункт 1\nСтарый текст\nПункт 3",
			cur:  "Пункт 1\nНовый текст\nПункт 3",
			want: []DiffLine{{"=", "Пункт 1"}, {"-", "Старый текст"}, {"+", "Новый текст"}, {"=", "Пункт 3"}},
		},
		{
			name: "blank lines preservation",
			old:  "a\n\nb",
			cur:  "a\n\nc",
			want: []DiffLine{{"=", "a"}, {"=", ""}, {"-", "b"}, {"+", "c"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Diff(tc.old, tc.cur)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Diff(%q, %q) =\n%+v\nwant:\n%+v", tc.old, tc.cur, got, tc.want)
			}
		})
	}
}

func TestDiff_LargeTextThreshold(t *testing.T) {
	// len(a) * len(b) > 4_000_000 triggers the fast path (all old deleted, all new added).
	// 2001 lines in a (strings.Split yields 2002 lines) * 2001 in b gives > 4_000_000.
	linesA := strings.Repeat("old line\n", 2001)
	linesB := strings.Repeat("new line\n", 2001)

	got := Diff(linesA, linesB)

	// strings.Split of "...\n" has 2002 entries (last is empty string)
	wantLen := 2002 + 2002
	if len(got) != wantLen {
		t.Fatalf("Diff threshold len = %d, want %d", len(got), wantLen)
	}

	for i := 0; i < 2002; i++ {
		if got[i].Op != "-" {
			t.Fatalf("expected '-' at index %d, got %+v", i, got[i])
		}
	}
	for i := 2002; i < wantLen; i++ {
		if got[i].Op != "+" {
			t.Fatalf("expected '+' at index %d, got %+v", i, got[i])
		}
	}
}
