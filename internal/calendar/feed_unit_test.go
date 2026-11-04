package calendar

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFoldLine(t *testing.T) {
	t.Run("short line", func(t *testing.T) {
		var b strings.Builder
		foldLine(&b, "SUMMARY:Short text")
		if got := b.String(); got != "SUMMARY:Short text\r\n" {
			t.Fatalf("got %q, want %q", got, "SUMMARY:Short text\r\n")
		}
	})

	t.Run("exactly 75 octets", func(t *testing.T) {
		var b strings.Builder
		line := strings.Repeat("A", 75)
		foldLine(&b, line)
		if got := b.String(); got != line+"\r\n" {
			t.Fatalf("got %q, want %q", got, line+"\r\n")
		}
	})

	t.Run("long ascii line folded at 75", func(t *testing.T) {
		var b strings.Builder
		line := strings.Repeat("A", 100)
		foldLine(&b, line)
		want := strings.Repeat("A", 75) + "\r\n " + strings.Repeat("A", 25) + "\r\n"
		if got := b.String(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("multibyte utf8 rune across 75 boundary", func(t *testing.T) {
		// 74 ASCII bytes + 2-byte Cyrillic 'Я' (0xD0, 0xAF) + more text
		var b strings.Builder
		line := strings.Repeat("A", 74) + "Я" + strings.Repeat("B", 20)
		foldLine(&b, line)
		got := b.String()

		// Verify every line in folded output is valid UTF-8
		parts := strings.Split(got, "\r\n")
		for i, p := range parts {
			if !utf8.ValidString(p) {
				t.Fatalf("part %d is not valid UTF-8: %q", i, p)
			}
		}

		// The split must not occur after 74th byte if it would cut 'Я' in half.
		// Instead, it cuts at 74 and puts 'Я' on the next line.
		want := strings.Repeat("A", 74) + "\r\n " + "Я" + strings.Repeat("B", 20) + "\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("multibyte 4-byte emoji across boundary", func(t *testing.T) {
		// 73 ASCII bytes + 4-byte emoji 🎉 (0xF0, 0x9F, 0x8E, 0x89)
		// 73 + 4 = 77 bytes. Boundary 75 hits middle of emoji (byte 2 of emoji).
		// Cut must retreat to 73.
		var b strings.Builder
		line := strings.Repeat("X", 73) + "🎉" + strings.Repeat("Y", 10)
		foldLine(&b, line)
		got := b.String()

		want := strings.Repeat("X", 73) + "\r\n " + "🎉" + strings.Repeat("Y", 10) + "\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestEscapeText(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain text", "plain text"},
		{`a\b`, `a\\b`},
		{"a;b", `a\;b`},
		{"a,b", `a\,b`},
		{"line1\r\nline2", `line1\nline2`},
		{"line1\nline2", `line1\nline2`},
		{`all;in,one\test` + "\n" + `and\more`, `all\;in\,one\\test\nand\\more`},
	}

	for _, tc := range cases {
		if got := escapeText(tc.in); got != tc.want {
			t.Errorf("escapeText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestColorName(t *testing.T) {
	// Palette exact matches
	paletteCases := map[string]string{
		"#86b100": "olivedrab",
		"#0092cc": "dodgerblue",
		"#00afc7": "darkturquoise",
		"#e89b06": "orange",
		"#00b38c": "mediumseagreen",
		"#de2b24": "crimson",
		"#bd7ac9": "orchid",
		"#838fa0": "slategray",
		"#c3612c": "chocolate",
		"#e97090": "palevioletred",
	}
	for hex, want := range paletteCases {
		if got := colorName(hex); got != want {
			t.Errorf("colorName(%s) = %s, want %s", hex, got, want)
		}
	}

	// Nearest CSS colors
	cssCases := map[string]string{
		"#ff0000": "red",
		"#000000": "black",
		"#008000": "green",
		"#1e90ff": "dodgerblue",
	}
	for hex, want := range cssCases {
		if got := colorName(hex); got != want {
			t.Errorf("colorName(%s) = %s, want %s", hex, got, want)
		}
	}
}

func TestICSDays(t *testing.T) {
	cases := []struct {
		days []int
		want string
	}{
		{[]int{1, 3, 5}, "MO,WE,FR"},
		{[]int{1, 2, 3, 4, 5, 6, 7}, "MO,TU,WE,TH,FR,SA,SU"},
		{[]int{0, 8, -1}, ""},
		{[]int{7, 1}, "SU,MO"},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := ICSDays(tc.days); got != tc.want {
			t.Errorf("ICSDays(%v) = %q, want %q", tc.days, got, tc.want)
		}
	}
}

func TestWriteTimes(t *testing.T) {
	var lines []string
	collector := func(l string) { lines = append(lines, l) }

	t.Run("timed event in UTC", func(t *testing.T) {
		lines = nil
		loc := time.FixedZone("UTC+3", 3*3600)
		start := time.Date(2026, 10, 28, 13, 0, 0, 0, loc)
		end := time.Date(2026, 10, 28, 14, 30, 0, 0, loc)
		writeTimes(collector, start, end, false)

		if len(lines) != 2 {
			t.Fatalf("expected 2 lines, got %d", len(lines))
		}
		if lines[0] != "DTSTART:20261028T100000Z" {
			t.Errorf("DTSTART = %q", lines[0])
		}
		if lines[1] != "DTEND:20261028T113000Z" {
			t.Errorf("DTEND = %q", lines[1])
		}
	})

	t.Run("all day event", func(t *testing.T) {
		lines = nil
		start := time.Date(2026, 10, 28, 0, 0, 0, 0, time.Local)
		end := time.Date(2026, 10, 29, 23, 59, 59, 0, time.Local)
		writeTimes(collector, start, end, true)

		if len(lines) != 2 {
			t.Fatalf("expected 2 lines, got %d", len(lines))
		}
		if lines[0] != "DTSTART;VALUE=DATE:20261028" {
			t.Errorf("DTSTART = %q", lines[0])
		}
		// DTEND is the day after the last one: 29 + 1 = 30
		if lines[1] != "DTEND;VALUE=DATE:20261030" {
			t.Errorf("DTEND = %q, want DTEND;VALUE=DATE:20261030", lines[1])
		}
	})
}
