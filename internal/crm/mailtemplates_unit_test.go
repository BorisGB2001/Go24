package crm

import (
	"testing"
)

func TestFillMailTemplate_Unit(t *testing.T) {
	tpl := MailTemplate{
		Subject: "Коммерческое предложение для {Title} ({Amount})",
		Body:    "<p>Уважаемый {Client}!</p>\nПредлагаем вам {Title}.<br>\n{UnknownField}\n{KnownMissing}",
	}

	values := map[string]string{
		"Title":  "ООО \"Ромашка & Ко\"",
		"Amount": "100 000 руб.",
		"Client": "Иван <Директор>",
	}

	known := func(k string) bool {
		return k == "KnownMissing"
	}

	subj, body := FillMailTemplate(tpl, values, known)

	// In subject: plain text without HTML escaping
	wantSubj := "Коммерческое предложение для ООО \"Ромашка & Ко\" (100 000 руб.)"
	if subj != wantSubj {
		t.Errorf("subject =\n%q\nwant:\n%q", subj, wantSubj)
	}

	// In body: HTML escaped values, KnownMissing replaced with empty string, UnknownField preserved
	wantBody := "<p>Уважаемый Иван &lt;Директор&gt;!</p>\nПредлагаем вам ООО &quot;Ромашка &amp; Ко&quot;.<br>\n{UnknownField}\n"
	if body != wantBody {
		t.Errorf("body =\n%q\nwant:\n%q", body, wantBody)
	}
}

func TestHtmlText_Unit(t *testing.T) {
	in := "A & B < C > D 'quotes' \"double\"\nNew line"
	got := htmlText(in)
	want := "A &amp; B &lt; C &gt; D &#39;quotes&#39; &quot;double&quot;<br>New line"
	if got != want {
		t.Errorf("htmlText() = %q, want %q", got, want)
	}
}
