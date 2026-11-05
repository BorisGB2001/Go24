package kb

import (
	"html/template"
	"reflect"
	"strings"
	"testing"
)

func TestSafeURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://example.com", true},
		{"http://example.com/path?q=1&v=2", true},
		{"mailto:user@example.com", true},
		{"/files/123-abc", true},
		{"/kb/space1/page2", true},
		{"https://example.com/search?a=1&amp;b=2", true},
		{"//attacker.com/evil", false},
		{"javascript:alert(1)", false},
		{"data:text/html;base64,PHNjcmlwdD4=", false},
		{"vbscript:msgbox(1)", false},
		{"ftp://ftp.example.com", false},
		{"file:///etc/passwd", false},
		{"relative/path", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			if got := safeURL(tc.url); got != tc.want {
				t.Fatalf("safeURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestCells(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{"| a | b | c |", []string{"a", "b", "c"}},
		{"|  col1  |   col2   |", []string{"col1", "col2"}},
		{"a | b", []string{"a", "b"}},
		{"| single |", []string{"single"}},
		{"|||", []string{"", ""}},
		{"", []string{""}},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			got := cells(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("cells(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

func TestAnchor(t *testing.T) {
	seen := map[string]int{}

	tests := []struct {
		text string
		want string
	}{
		{"Hello World", "hello-world"},
		{"Инструкция по безопасности", "инструкция-по-безопасности"},
		{"Section 1.2: Details", "section-1-2-details"},
		{"??? !!!", "h"},
		{"Hello World", "hello-world-2"},
		{"Hello World", "hello-world-3"},
		{"???", "h-2"},
		{"--leading-and-trailing--", "leading-and-trailing"},
		{"Multiple   spaces   between", "multiple-spaces-between"},
	}

	for _, tc := range tests {
		got := anchor(tc.text, seen)
		if got != tc.want {
			t.Fatalf("anchor(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestInline(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "code span",
			in:   "`x := 10`",
			want: "<code>x := 10</code>",
		},
		{
			name: "bold text",
			in:   "**important note**",
			want: "<b>important note</b>",
		},
		{
			name: "italic text",
			in:   "*slanted note*",
			want: "<i>slanted note</i>",
		},
		{
			name: "struck text",
			in:   "~~deprecated~~",
			want: "<s>deprecated</s>",
		},
		{
			name: "safe http link",
			in:   "[Site](https://example.com)",
			want: `<a href="https://example.com" target="_blank" rel="noopener">Site</a>`,
		},
		{
			name: "safe internal link",
			in:   "[Doc](/kb/sp1/p2)",
			want: `<a href="/kb/sp1/p2">Doc</a>`,
		},
		{
			name: "safe mailto link",
			in:   "[Help](mailto:support@example.com)",
			want: `<a href="mailto:support@example.com">Help</a>`,
		},
		{
			name: "unsafe link fallback",
			in:   "[Evil](javascript:void)",
			want: "Evil",
		},
		{
			name: "unsafe link with nested parens",
			in:   "[Evil](javascript:alert(1))",
			want: "Evil)",
		},
		{
			name: "safe external image",
			in:   "![Diagram](https://img.com/diag.png)",
			want: `<img src="https://img.com/diag.png" alt="Diagram" loading="lazy">`,
		},
		{
			name: "safe internal image",
			in:   "![Photo](/files/abc?inline=1)",
			want: `<img src="/files/abc?inline=1" alt="Photo" loading="lazy">`,
		},
		{
			name: "unsafe image fallback",
			in:   "![Photo](javascript:bad)",
			want: "Photo",
		},
		{
			name: "unsafe image with nested parens",
			in:   "![Photo](javascript:bad())",
			want: "Photo)",
		},
		{
			name: "mailto image disallowed",
			in:   "![Avatar](mailto:test@x.com)",
			want: "Avatar",
		},
		{
			name: "bold with nested code",
			in:   "**`code`**",
			want: "<b><code>code</code></b>",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inline(tc.in)
			if got != tc.want {
				t.Fatalf("inline(%q) =\n%q\nwant:\n%q", tc.in, got, tc.want)
			}
		})
	}

	// Test line with more than 10 code spans to ensure rune indexing beyond '9' behaves correctly.
	t.Run("more than ten code spans", func(t *testing.T) {
		var inParts, wantParts []string
		for i := 0; i < 15; i++ {
			inParts = append(inParts, "`code`")
			wantParts = append(wantParts, "<code>code</code>")
		}
		in := strings.Join(inParts, " ")
		want := strings.Join(wantParts, " ")
		if got := inline(in); got != want {
			t.Fatalf("inline with 15 spans =\n%q\nwant:\n%q", got, want)
		}
	})
}

func TestRender_Elements(t *testing.T) {
	t.Run("headings levels", func(t *testing.T) {
		md := "# Title 1\n## Title 2\n### Title 3"
		got := string(Render(md))
		for _, want := range []string{
			`<h2 id="title-1">Title 1</h2>`,
			`<h3 id="title-2">Title 2</h3>`,
			`<h4 id="title-3">Title 3</h4>`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %s", want, got)
			}
		}
	})

	t.Run("paragraphs and line breaks", func(t *testing.T) {
		md := "First line\nSecond line in same paragraph\n\nNew paragraph"
		got := string(Render(md))
		want := "<p>First line<br>Second line in same paragraph</p>\n<p>New paragraph</p>\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("horizontal rules", func(t *testing.T) {
		for _, rule := range []string{"---", "***", "___", "-----", "******"} {
			got := string(Render("Above\n" + rule + "\nBelow"))
			if !strings.Contains(got, "<hr>\n") {
				t.Fatalf("rule %q did not produce <hr>: %s", rule, got)
			}
		}
	})

	t.Run("code blocks escape html and ignore markdown", func(t *testing.T) {
		md := "```go\n<script>alert(1)</script>\n# Not a heading\n**not bold**\n```"
		got := string(Render(md))
		want := "<pre><code>&lt;script&gt;alert(1)&lt;/script&gt;\n# Not a heading\n**not bold**</code></pre>\n"
		if got != want {
			t.Fatalf("code block rendered =\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("unclosed code block until eof", func(t *testing.T) {
		md := "```\nsome code line 1\nsome code line 2"
		got := string(Render(md))
		want := "<pre><code>some code line 1\nsome code line 2</code></pre>\n"
		if got != want {
			t.Fatalf("unclosed code block =\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("unordered and ordered lists", func(t *testing.T) {
		md := "- item 1\n* item 2\n\n1. first\n2. second\n3) third"
		got := string(Render(md))
		for _, want := range []string{
			"<ul>\n<li>item 1</li>\n<li>item 2</li>\n</ul>",
			"<ol>\n<li>first</li>\n<li>second</li>\n<li>third</li>\n</ol>",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %s", want, got)
			}
		}
	})

	t.Run("checklists mark variants", func(t *testing.T) {
		md := "- [ ] todo\n- [x] done small\n- [X] done capital\n- [х] done cyrillic small\n- [Х] done cyrillic capital"
		got := string(Render(md))
		if !strings.Contains(got, `<li class="check"><input type="checkbox" disabled> todo</li>`) {
			t.Errorf("missing unchecked in %s", got)
		}
		if n := strings.Count(got, `<input type="checkbox" checked disabled>`); n != 4 {
			t.Errorf("expected 4 checked boxes, got %d in %s", n, got)
		}
	})

	t.Run("blockquotes", func(t *testing.T) {
		md := "> quote text\n>\n> second quote"
		got := string(Render(md))
		for _, want := range []string{
			"<blockquote>quote text</blockquote>",
			"<blockquote></blockquote>",
			"<blockquote>second quote</blockquote>",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %s", want, got)
			}
		}
	})

	t.Run("tables padding and truncation", func(t *testing.T) {
		md := "| Head 1 | Head 2 |\n|---|---|\n| Only 1 |\n| C1 | C2 | Extra 3 |"
		got := string(Render(md))
		for _, want := range []string{
			"<thead><tr><th>Head 1</th><th>Head 2</th></tr></thead>",
			"<tr><td>Only 1</td><td></td></tr>",
			"<tr><td>C1</td><td>C2</td></tr>",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %s", want, got)
			}
		}
		if strings.Contains(got, "Extra 3") {
			t.Errorf("cell beyond head length should be omitted, found in: %s", got)
		}
	})

	t.Run("html sanitization", func(t *testing.T) {
		md := "<script>alert(1)</script>\n<iframe src=\"//evil.com\"></iframe>\n[xss](javascript:alert)\n[proto](//evil.com)"
		got := string(Render(md))
		for _, bad := range []string{"<script>", "<iframe", "href=\"javascript:", "href=\"//evil.com\""} {
			if strings.Contains(got, bad) {
				t.Fatalf("found unsafe string %q in rendered output:\n%s", bad, got)
			}
		}
	})
}

func TestHeadings_Extraction(t *testing.T) {
	md := "# Alpha\n## Beta\n```\n# Ignored in code\n```\n### Gamma\n# Alpha"
	got := Headings(md)

	want := []Heading{
		{Level: 1, Text: "Alpha", ID: "alpha"},
		{Level: 2, Text: "Beta", ID: "beta"},
		{Level: 3, Text: "Gamma", ID: "gamma"},
		{Level: 1, Text: "Alpha", ID: "alpha-2"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Headings =\n%+v\nwant:\n%+v", got, want)
	}
}

func TestMark_Snippet(t *testing.T) {
	tests := []struct {
		name string
		text string
		q    string
		want template.HTML
	}{
		{
			name: "empty query returns escaped text",
			text: "Hello <b>world</b>",
			q:    "",
			want: "Hello &lt;b&gt;world&lt;/b&gt;",
		},
		{
			name: "whitespace query returns escaped text",
			text: "Hello world",
			q:    "   \t  ",
			want: "Hello world",
		},
		{
			name: "case insensitive multiple matches",
			text: "Go Go24 gO",
			q:    "go",
			want: "<mark>Go</mark> <mark>Go</mark>24 <mark>gO</mark>",
		},
		{
			name: "regex special characters in query quoted literally",
			text: "Price: $100.00 [tax? yes]",
			q:    "$100.00",
			want: "Price: <mark>$100.00</mark> [tax? yes]",
		},
		{
			name: "escapes html in text and matches",
			text: "<script>alert('ok')</script>",
			q:    "alert",
			want: "&lt;script&gt;<mark>alert</mark>(&#39;ok&#39;)&lt;/script&gt;",
		},
		{
			name: "query matching html tags",
			text: "Tags: <div> and <span>",
			q:    "<div>",
			want: "Tags: <mark>&lt;div&gt;</mark> and &lt;span&gt;",
		},
		{
			name: "cyrillic matching",
			text: "Инструкция по настройке системы",
			q:    "настройке",
			want: "Инструкция по <mark>настройке</mark> системы",
		},
		{
			name: "no match found",
			text: "Ordinary text",
			q:    "missing",
			want: "Ordinary text",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Mark(tc.text, tc.q)
			if got != tc.want {
				t.Fatalf("Mark(%q, %q) =\n%q\nwant:\n%q", tc.text, tc.q, got, tc.want)
			}
		})
	}
}
