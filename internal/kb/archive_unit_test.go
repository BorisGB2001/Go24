package kb

import (
	"archive/zip"
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Normal Name", "Normal Name"},
		{"Отпуск и отгулы", "Отпуск и отгулы"},
		{"a/b\\c:d*e?f\"g<h>i|j", "a_b_c_d_e_f_g_h_i_j"},
		{"  ..spaced and dotted..  ", "spaced and dotted"},
		{"...", "_"},
		{"", "_"},
		{"control\x00\x07char", "control__char"},
		{
			in:   strings.Repeat("a", 150),
			want: strings.Repeat("a", 100),
		},
		{
			// Multibyte Cyrillic runes: exactly 100 runes must be kept, not sliced mid-rune.
			in:   strings.Repeat("я", 120),
			want: strings.Repeat("я", 100),
		},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := safeName(tc.in)
			if got != tc.want {
				t.Fatalf("safeName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEscapePath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"path/to/page.md", "path/to/page.md"},
		{"01 Section/02 Page.md", "01%20Section/02%20Page.md"},
		{"отдел/план.md", "%D0%BE%D1%82%D0%B4%D0%B5%D0%BB/%D0%BF%D0%BB%D0%B0%D0%BD.md"},
		{"single.md", "single.md"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := escapePath(tc.in)
			if got != tc.want {
				t.Fatalf("escapePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDigits(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"123abc456", "123"},
		{"9999", "9999"},
		{"abc123", ""},
		{"", ""},
		{"007bond", "007"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := digits(tc.in)
			if got != tc.want {
				t.Fatalf("digits(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNatLess_Comparison(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"2", "10", true},
		{"10", "2", false},
		{"01", "2", true},
		{"02", "10", true},
		{"item 2", "item 10", true},
		{"item 10", "item 2", false},
		{"item 1 a", "item 1 b", true},
		{"apple", "Banana", true},
		{"banana", "Apple", false},
		{"test", "testing", true},
		{"testing", "test", false},
		{"", "nonempty", true},
		{"nonempty", "", false},
		{"same", "same", false},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tc.a, tc.b), func(t *testing.T) {
			got := natLess(tc.a, tc.b)
			if got != tc.want {
				t.Fatalf("natLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestParseMD_Cases(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		fallback  string
		wantTitle string
		wantBody  string
	}{
		{
			name:      "yaml double quotes with escaped quotes",
			in:        "---\ntitle: \"Page with \\\"quotes\\\"\"\n---\n\nBody content",
			fallback:  "fallback",
			wantTitle: `Page with "quotes"`,
			wantBody:  "Body content",
		},
		{
			name:      "yaml single quotes with doubled quotes",
			in:        "---\ntitle: 'It''s a title'\n---\nBody",
			fallback:  "fallback",
			wantTitle: "It's a title",
			wantBody:  "Body",
		},
		{
			name:      "yaml unquoted title",
			in:        "---\ntitle: Simple Title\n---\nBody",
			fallback:  "fallback",
			wantTitle: "Simple Title",
			wantBody:  "Body",
		},
		{
			name: "invalid front matter with five dashes not parsed as front matter",
			in: `-----
date: 2026-10-28
title: "My Note"
tags: [work, report]
---

First paragraph of text.`,
			fallback:  "fallback",
			wantTitle: "fallback",
			wantBody: `-----
date: 2026-10-28
title: "My Note"
tags: [work, report]
---

First paragraph of text.`,
		},
		{
			name: "proper yaml front matter with other fields",
			in: `---
date: 2026-10-28
title: "My Note"
tags: [work, report]
---

First paragraph of text.`,
			fallback:  "fallback",
			wantTitle: "My Note",
			wantBody:  "First paragraph of text.",
		},
		{
			name:      "first line heading without front matter",
			in:        "# First Line Heading\n\nParagraph text",
			fallback:  "fallback",
			wantTitle: "First Line Heading",
			wantBody:  "Paragraph text",
		},
		{
			name:      "bom prefix and crlf normalization",
			in:        "\ufeff# Heading\r\n\r\nLine 1\r\nLine 2",
			fallback:  "fallback",
			wantTitle: "Heading",
			wantBody:  "Line 1\nLine 2",
		},
		{
			name:      "fallback title when no front matter or heading",
			in:        "Plain text without any markdown header",
			fallback:  "Default File Name",
			wantTitle: "Default File Name",
			wantBody:  "Plain text without any markdown header",
		},
		{
			name:      "empty front matter title falls back to heading",
			in:        "---\ntitle: \"\"\n---\n\n# Fallback Heading\nText",
			fallback:  "file",
			wantTitle: "Fallback Heading",
			wantBody:  "Text",
		},
		{
			name:      "empty text returns fallback and empty body",
			in:        "",
			fallback:  "untitled",
			wantTitle: "untitled",
			wantBody:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotTitle, gotBody := parseMD([]byte(tc.in), tc.fallback)
			if gotTitle != tc.wantTitle || gotBody != tc.wantBody {
				t.Fatalf("parseMD() =\nTitle: %q, Body: %q\nwant:\nTitle: %q, Body: %q",
					gotTitle, gotBody, tc.wantTitle, tc.wantBody)
			}
		})
	}
}

func TestReadArchive_Unpack(t *testing.T) {
	t.Run("valid archive with files and directories", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)

		// Regular file
		f1, err := zw.Create("01 Intro.md")
		if err != nil {
			t.Fatal(err)
		}
		f1.Write([]byte("# Intro"))

		// Subdirectory file
		f2, err := zw.Create("01 Intro/sub.md")
		if err != nil {
			t.Fatal(err)
		}
		f2.Write([]byte("Sub content"))

		// Directory entry (should be skipped)
		_, err = zw.Create("01 Intro/")
		if err != nil {
			t.Fatal(err)
		}

		// MacOS metadata folder entry (should be skipped)
		fMac, err := zw.Create("__MACOSX/._01 Intro.md")
		if err != nil {
			t.Fatal(err)
		}
		fMac.Write([]byte("mac metadata"))

		// Hidden dotfile (should be skipped)
		fDot, err := zw.Create(".DS_Store")
		if err != nil {
			t.Fatal(err)
		}
		fDot.Write([]byte("dotfile"))

		// Windows backslash path
		fWin, err := zw.Create("folder\\win.md")
		if err != nil {
			t.Fatal(err)
		}
		fWin.Write([]byte("win content"))

		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}

		got, err := ReadArchive(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatalf("ReadArchive unexpected error: %v", err)
		}

		wantNames := []string{
			"01 Intro.md",
			"01 Intro/sub.md",
			"folder/win.md",
		}

		var gotNames []string
		for _, f := range got {
			gotNames = append(gotNames, f.Name)
		}

		if !reflect.DeepEqual(gotNames, wantNames) {
			t.Fatalf("got archive files: %v, want: %v", gotNames, wantNames)
		}
	})

	t.Run("invalid zip bytes returns error", func(t *testing.T) {
		garbage := []byte("this is not a zip file")
		_, err := ReadArchive(bytes.NewReader(garbage), int64(len(garbage)))
		if err == nil {
			t.Fatal("expected error on invalid zip, got nil")
		}
	})

	t.Run("exceeding max entries returns error", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)

		// Create 2001 files (importMaxEntries is 2000)
		for i := 0; i < 2001; i++ {
			f, err := zw.Create(fmt.Sprintf("f%d.md", i))
			if err != nil {
				t.Fatal(err)
			}
			f.Write([]byte("x"))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}

		_, err := ReadArchive(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err == nil {
			t.Fatal("expected error on > 2000 entries, got nil")
		}
	})
}
