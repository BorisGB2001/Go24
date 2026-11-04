package crm

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestDictText_Unit(t *testing.T) {
	tr := func(key string) string {
		if key == "crm.dict.call" {
			return "Звонок"
		}
		return key
	}

	// 1. Explicit name takes precedence
	d1 := Dict{Code: "call", Name: "Входящий звонок"}
	if got := d1.Text(tr); got != "Входящий звонок" {
		t.Errorf("Text() with name = %q, want \"Входящий звонок\"", got)
	}

	// 2. Code translated when name is empty
	d2 := Dict{Code: "call", Name: ""}
	if got := d2.Text(tr); got != "Звонок" {
		t.Errorf("Text() with code and tr = %q, want \"Звонок\"", got)
	}

	// 3. Name takes precedence even if tr is nil
	if got := d1.Text(nil); got != "Входящий звонок" {
		t.Errorf("Text() with name and nil tr = %q, want \"Входящий звонок\"", got)
	}

	// 4. Empty code and empty name returns empty string even if tr is nil
	dEmpty := Dict{}
	if got := dEmpty.Text(nil); got != "" {
		t.Errorf("Text() empty = %q, want empty", got)
	}
}

func TestDictSQLHelpers_Unit(t *testing.T) {
	if got := dictCols("j"); got != "coalesce(j.id::text, ''), coalesce(j.code, ''), coalesce(j.name, '')" {
		t.Errorf("dictCols(\"j\") = %q", got)
	}

	if got := dictJoin("jt", "d.deal_type_id"); got != " LEFT JOIN crm_dict jt ON jt.id = d.deal_type_id" {
		t.Errorf("dictJoin() = %q", got)
	}
}

func TestDictName_Unit(t *testing.T) {
	// Valid name
	name := "  Холодный звонок  "
	if err := dictName(&name); err != nil || name != "Холодный звонок" {
		t.Errorf("dictName valid failed: name=%q, err=%v", name, err)
	}

	// Empty name returns err.crm_title
	empty := "   "
	if err := dictName(&empty); err == nil {
		t.Fatal("expected error on empty dictName, got nil")
	} else {
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.crm_title" {
			t.Errorf("expected err.crm_title, got %v", err)
		}
	}

	// Name exceeding 100 runes returns err.crm_field
	longName := strings.Repeat("d", 101)
	if err := dictName(&longName); err == nil {
		t.Fatal("expected error on dictName > 100 runes, got nil")
	} else {
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.crm_field" {
			t.Errorf("expected err.crm_field, got %v", err)
		}
	}
}
