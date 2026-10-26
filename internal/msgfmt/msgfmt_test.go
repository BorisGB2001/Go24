package msgfmt

import "testing"

func TestHTML(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"просто текст", "просто текст"},
		{"a < b & \"c\"", "a &lt; b &amp; &#34;c&#34;"},
		{"две\nстроки", "две<br>строки"},
		{"*жирный* и _курсив_ и ~зачёркнутый~", "<b>жирный</b> и <i>курсив</i> и <s>зачёркнутый</s>"},
		{"**жирный** __курсив__ ~~нет~~", "<b>жирный</b> <i>курсив</i> <s>нет</s>"},
		{"[b]Ж[/b] [i]К[/i] [u]П[/u] [S]З[/S] [b][i]оба[/i][/b]", "<b>Ж</b> <i>К</i> <u>П</u> <s>З</s> <b><i>оба</i></b>"},
		{"snake_case_name и 2*3*4", "snake_case_name и 2*3*4"},
		{"* не список *", "* не список *"},
		{"'*в кавычках*'", "&#39;<b>в кавычках</b>&#39;"},
		{"код `a*b*c <x>`", "код <code>a*b*c &lt;x&gt;</code>"},
		{"см. https://go24.lan/tasks?a=1&b=2.", `см. <a href="https://go24.lan/tasks?a=1&amp;b=2" target="_blank" rel="noopener">https://go24.lan/tasks?a=1&amp;b=2</a>.`},
		{"(www.ru/a_b_c)", `(<a href="https://www.ru/a_b_c" target="_blank" rel="noopener">www.ru/a_b_c</a>)`},
		{"[url=https://portal.ru]корпоративного портала[/url]", `<a href="https://portal.ru" target="_blank" rel="noopener">корпоративного портала</a>`},
		{"[url]http://x.ru[/url]", `<a href="http://x.ru" target="_blank" rel="noopener">http://x.ru</a>`},
		{"[url=javascript:alert(1)]x[/url]", "[url=javascript:alert(1)]x[/url]"},
		{"до\n```go\nfunc *a*() {}\n```\nпосле", "до<pre><code>func *a*() {}</code></pre>после"},
		{"[code]<b>[/code]", "<pre><code>&lt;b&gt;</code></pre>"},
		{"> цитата\n> *вторая*\nответ", "<blockquote>цитата<br><b>вторая</b></blockquote>ответ"},
		{"[quote]кто-то\nсказал[/quote]\nда", "<blockquote>кто-то<br>сказал</blockquote>да"},
		{"\x00", ""},
	} {
		if got := HTML(c.in); got != c.want {
			t.Errorf("HTML(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestPlain(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"*жирный* [b]Ж[/b] [url=https://x.ru]x[/url] snake_case", "жирный Ж x snake_case"},
		{"```код```", "код"},
	} {
		if got := Plain(c.in); got != c.want {
			t.Errorf("Plain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
