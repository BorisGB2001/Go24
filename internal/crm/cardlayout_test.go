package crm

import (
	"reflect"
	"testing"
)

// TestCardLayoutClean: «Настроить карточку» keeps the known fields once each; a field not placed — a new custom field
// to the last section, a built-in one to the first; an unknown or another kind's field drops out; empty sections stay.
func TestCardLayoutClean(t *testing.T) {
	def := DefaultCardLayout("deal", []string{"7"})
	if len(def.Sections) != 2 || def.Sections[0].Code != "main" || def.Sections[1].Code != "more" ||
		!reflect.DeepEqual(def.Sections[1].Fields, []string{"f:7"}) || def.Sections[0].Fields[0] != "deal_type" {
		t.Fatalf("default %+v", def)
	}
	if DefaultCardLayout("type:x", nil).Sections[0].Fields[0] != "origin" {
		t.Fatal("a smart process has no deal type")
	}
	l := CardLayout{Sections: []CardSection{
		{Code: "main", Fields: []string{"amount", "amount", "phone", "responsible"}},
		{Name: " Клиент ", Fields: []string{"company", "contacts", "f:9"}},
		{Name: "Пусто", Fields: nil},
	}, Hidden: []string{"comment", "f:7", "password"}}
	got := l.clean("deal", []string{"7", "8"})
	want := CardLayout{Sections: []CardSection{
		{Code: "main", Fields: []string{"amount", "responsible", "deal_type", "origin", "close_date", "observers", "utm", "created"}},
		{Name: "Клиент", Fields: []string{"company", "contacts"}},
		{Name: "Пусто", Fields: []string{"f:8"}},
	}, Hidden: []string{"comment", "f:7"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clean\n got %+v\nwant %+v", got, want)
	}
	if got := (CardLayout{}).clean("company", nil); len(got.Sections) != 1 || len(got.Sections[0].Fields) != len(CardFields["company"]) {
		t.Fatalf("no sections %+v", got)
	}
}
