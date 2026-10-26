package kb

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	got := string(Render("# Отпуск\nКак **оформить** *отпуск*: `заявление`\n\n- пункт [раз](https://x.ru)\n- пункт [два](javascript:alert(1))\n1. первый\n\n> цитата\n```\n<b>как есть</b>\n```\n<script>alert(1)</script>"))
	for _, want := range []string{`<h2 id="отпуск">Отпуск</h2>`, "<b>оформить</b>", "<i>отпуск</i>", "<code>заявление</code>",
		`<ul>`, `<a href="https://x.ru" target="_blank" rel="noopener">раз</a>`, "<li>пункт два", "<ol>", "<li>первый</li>",
		"<blockquote>цитата</blockquote>", "<pre><code>&lt;b&gt;как есть&lt;/b&gt;</code></pre>", "&lt;script&gt;"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") || strings.Contains(got, "javascript:") {
		t.Fatalf("unsafe output:\n%s", got)
	}
	if got := string(Render("**`a*b`**")); got != "<p><b><code>a*b</code></b></p>\n" {
		t.Fatalf("code inside bold = %q", got)
	}
	got = string(Render("Схема: ![план *этажа*](/files/abc?inline=1) и ![x](javascript:alert(1)) [файл](/files/def)"))
	if !strings.Contains(got, `<img src="/files/abc?inline=1" alt="план *этажа*" loading="lazy">`) ||
		!strings.Contains(got, `<a href="/files/def">файл</a>`) || strings.Contains(got, "javascript") {
		t.Fatalf("pictures = %q", got)
	}
	got = string(Render("Было ~~старое~~ правило\n---\n- [ ] позвонить\n- [x] написать *письмо*\n\n| Кто | Что |\n|:---|---:|\n| Анна | **отпуск** |\n| Ева |\n| <b> | x | лишняя |\nпосле"))
	for _, want := range []string{"<s>старое</s>", "<hr>", `<li class="check"><input type="checkbox" disabled> позвонить</li>`,
		`<li class="check"><input type="checkbox" checked disabled> написать <i>письмо</i></li>`,
		"<thead><tr><th>Кто</th><th>Что</th></tr></thead>", "<tr><td>Анна</td><td><b>отпуск</b></td></tr>",
		"<tr><td>Ева</td><td></td></tr>", "<tr><td>&lt;b&gt;</td><td>x</td></tr>", "</table>\n<p>после</p>"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if got := string(Render("| не таблица |\nтекст")); !strings.HasPrefix(got, "<p>| не таблица |<br>текст") {
		t.Fatalf("a row without the line under the head = %q", got)
	}
}

func TestHeadings(t *testing.T) {
	md := "# Отпуск и **отгулы**\nтекст\n## Как оформить\n```\n# не заголовок\n```\n### Как оформить\n## Как оформить\n# !!!"
	got := Headings(md)
	want := []Heading{{1, "Отпуск и отгулы", "отпуск-и-отгулы"}, {2, "Как оформить", "как-оформить"},
		{3, "Как оформить", "как-оформить-2"}, {2, "Как оформить", "как-оформить-3"}, {1, "!!!", "h"}}
	if len(got) != len(want) {
		t.Fatalf("headings = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("heading %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if html := string(Render(md)); !strings.Contains(html, `<h4 id="как-оформить-2">Как оформить</h4>`) ||
		!strings.Contains(html, `<h2 id="отпуск-и-отгулы">Отпуск и <b>отгулы</b></h2>`) {
		t.Fatalf("anchors in\n%s", html)
	}
}

func TestMark(t *testing.T) {
	if got := string(Mark("Отпуск <b> и ОТПУСКные", "отпуск")); got != "<mark>Отпуск</mark> &lt;b&gt; и <mark>ОТПУСК</mark>ные" {
		t.Fatalf("Mark = %q", got)
	}
	if got := string(Mark("a.b", ".")); got != "a<mark>.</mark>b" {
		t.Fatalf("Mark of a regexp sign = %q", got)
	}
}
