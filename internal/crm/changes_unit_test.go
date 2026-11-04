package crm

import (
	"strings"
	"testing"
	"time"
)

func TestChangesParse_Unit(t *testing.T) {
	jsonBody := `[{"f":"title","o":"Old Title","n":"New Title"},{"f":"amount","o":"100","n":"200"}]`
	list := Changes(jsonBody)
	if len(list) != 2 {
		t.Fatalf("Changes len = %d, want 2", len(list))
	}
	if list[0].Field != "title" || list[0].Old != "Old Title" || list[0].New != "New Title" {
		t.Errorf("change 0 mismatch: %+v", list[0])
	}

	// Invalid json returns nil without panic
	if got := Changes("not-json"); got != nil {
		t.Errorf("Changes on invalid json = %v, want nil", got)
	}
}

func TestClip_Unit(t *testing.T) {
	short := "Краткий комментарий"
	if got := clip(short); got != short {
		t.Errorf("clip(short) = %q, want %q", got, short)
	}

	exact300 := strings.Repeat("ж", 300)
	if got := clip(exact300); got != exact300 {
		t.Errorf("clip(300) = %q, want unclipped", got)
	}

	over300 := strings.Repeat("ж", 305)
	gotOver := clip(over300)
	wantOver := strings.Repeat("ж", 300) + "…"
	if gotOver != wantOver {
		t.Errorf("clip(305) len=%d, want 300 runes + ellipsis", len([]rune(gotOver)))
	}
}

func TestDifferHelpers_Unit(t *testing.T) {
	// 1. differ.add
	var d differ
	d.add("field1", "same", "same") // should not be added
	d.add("field2", "old", "new")   // should be added
	if len(d) != 1 || d[0].Field != "field2" || d[0].Old != "old" || d[0].New != "new" {
		t.Errorf("differ mismatch: %+v", d)
	}

	// 2. dictValue
	if got := dictValue(Dict{Code: "sale", Name: ""}); got != "code:sale" {
		t.Errorf("dictValue code only = %q, want \"code:sale\"", got)
	}
	if got := dictValue(Dict{Code: "sale", Name: "Продажа"}); got != "Продажа" {
		t.Errorf("dictValue with name = %q, want \"Продажа\"", got)
	}
	if got := dictValue(Dict{}); got != "" {
		t.Errorf("dictValue empty = %q, want empty", got)
	}

	// 3. dateValue
	if got := dateValue(nil); got != "" {
		t.Errorf("dateValue(nil) = %q, want empty", got)
	}
	tm := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	if got := dateValue(&tm); got != "date:2026-10-28" {
		t.Errorf("dateValue(%v) = %q, want \"date:2026-10-28\"", tm, got)
	}

	// 4. refNames
	refs := []Ref{{Name: "Иван"}, {Name: "Ольга"}}
	if got := refNames(refs); got != "Иван, Ольга" {
		t.Errorf("refNames() = %q, want \"Иван, Ольга\"", got)
	}
	if got := refNames(nil); got != "" {
		t.Errorf("refNames(nil) = %q, want empty", got)
	}
}

func TestEntityChanges_Unit(t *testing.T) {
	d1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	// 1. dealChanges
	dealA := Deal{
		Title:       "Сделка А",
		Amount:      1000,
		Currency:    "RUB",
		CloseDate:   &d1,
		Responsible: Ref{Name: "Менеджер 1"},
	}
	dealB := Deal{
		Title:       "Сделка Б",
		Amount:      2000,
		Currency:    "USD",
		CloseDate:   &d2,
		Responsible: Ref{Name: "Менеджер 2"},
	}
	chDeals := dealChanges(dealA, dealB)
	expectedFields := []string{"title", "amount", "currency", "responsible", "close_date"}
	for _, f := range expectedFields {
		found := false
		for _, ch := range chDeals {
			if ch.Field == f {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("dealChanges missing change for %q: %+v", f, chDeals)
		}
	}

	// 2. leadChanges
	leadA := Lead{Title: "Лид А", Name: "Иван", Phone: "+7900"}
	leadB := Lead{Title: "Лид Б", Name: "Иван", Phone: "+7901"}
	chLeads := leadChanges(leadA, leadB)
	if len(chLeads) != 2 { // title and phone changed, name didn't
		t.Fatalf("leadChanges len = %d, want 2: %+v", len(chLeads), chLeads)
	}

	// 3. contactChanges
	contactA := Contact{Name: "Анна", Position: "Бухгалтер"}
	contactB := Contact{Name: "Анна", Position: "Главный бухгалтер"}
	chContacts := contactChanges(contactA, contactB)
	if len(chContacts) != 1 || chContacts[0].Field != "position" {
		t.Errorf("contactChanges mismatch: %+v", chContacts)
	}

	// 4. companyChanges
	compA := Company{Title: "Рога", Website: "roga.ru"}
	compB := Company{Title: "Рога и Копыта", Website: "kopyta.ru"}
	chCompanies := companyChanges(compA, compB)
	if len(chCompanies) != 2 {
		t.Errorf("companyChanges mismatch: %+v", chCompanies)
	}
}

func TestLogChangesEmpty_Unit(t *testing.T) {
	// Ensure calling with empty slice returns nil immediately without db query
	s := &Store{}
	if err := s.LogChanges(nil, "user-1", "deal", "deal-1", nil); err != nil {
		t.Errorf("LogChanges with nil list returned error: %v", err)
	}
	if err := s.LogChanges(nil, "user-1", "deal", "deal-1", []Change{}); err != nil {
		t.Errorf("LogChanges with empty list returned error: %v", err)
	}
}
