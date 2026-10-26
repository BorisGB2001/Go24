package web

import (
	"html/template"
	"strings"
	"testing"

	"go24/internal/i18n"
)

// TestCRMChangesRender: a «changed» event reads field by field: the locale's name of a field or an own field's name,
// a built-in stage or item by the locale, a date in the language's layout, none as a dash; the values are escaped.
func TestCRMChangesRender(t *testing.T) {
	b, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	f := langFuncs(b, "ru-RU")["crmChanges"].(func(string) template.HTML)
	got := string(f(`[{"f":"amount","o":"100","n":"250"},{"f":"deal_type","o":"","n":"code:SERVICE"},` +
		`{"f":"close_date","o":"","n":"date:2026-11-30"},{"f":"uf:Договор","o":"<b>","n":"code:X"}]`))
	for _, want := range []string{"Изменено поле «Сумма»: <s class=\"muted\">100</s> → <b>250</b>", "<s class=\"muted\">—</s>",
		"<b>" + b.T("ru-RU", "crm.dict.SERVICE") + "</b>", "<b>30.11.2026</b>", "«Договор»: <s class=\"muted\">&lt;b&gt;</s> → <b>code:X</b>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in %s", want, got)
		}
	}
}
