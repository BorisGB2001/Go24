package crm

import (
	"errors"
	"reflect"
	"testing"

	"go24/internal/tasks"
)

func TestRequiredError_Unit(t *testing.T) {
	reqErr := &RequiredError{
		Fields: []tasks.FieldValue{
			{Field: tasks.Field{Name: "Город"}},
			{Field: tasks.Field{Name: "ИНН"}},
		},
	}

	if got := reqErr.Error(); got != "crm: required fields are empty" {
		t.Errorf("Error() = %q, want \"crm: required fields are empty\"", got)
	}

	i18nErr := reqErr.I18n()
	if i18nErr.Key != "err.crm_required" {
		t.Errorf("I18n().Key = %q, want \"err.crm_required\"", i18nErr.Key)
	}
	if !reflect.DeepEqual(i18nErr.Args, []any{"Город, ИНН"}) {
		t.Errorf("I18n().Args = %v, want [\"Город, ИНН\"]", i18nErr.Args)
	}

	// Single field
	singleErr := (&RequiredError{
		Fields: []tasks.FieldValue{{Field: tasks.Field{Name: "Сумма"}}},
	}).I18n()
	if !reflect.DeepEqual(singleErr.Args, []any{"Сумма"}) {
		t.Errorf("single Field Args = %v, want [\"Сумма\"]", singleErr.Args)
	}

	// Empty fields
	emptyErr := (&RequiredError{}).I18n()
	if !reflect.DeepEqual(emptyErr.Args, []any{""}) {
		t.Errorf("empty Field Args = %v, want [\"\"]", emptyErr.Args)
	}

	// Check type assertion
	var target *RequiredError
	if !errors.As(reqErr, &target) {
		t.Error("expected errors.As to match *RequiredError")
	}
}
