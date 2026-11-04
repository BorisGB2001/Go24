package crm

import (
	"errors"
	"testing"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

func TestFormFieldCustom_Unit(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"f:42", "42"},
		{"f:custom_field_id", "custom_field_id"},
		{"name", ""},
		{"email", ""},
		{"f:", ""},
		{"", ""},
	}
	for _, tt := range tests {
		ff := FormField{Key: tt.key}
		if got := ff.Custom(); got != tt.want {
			t.Errorf("FormField{Key: %q}.Custom() = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestFormFieldSet_Unit(t *testing.T) {
	if got := FormFieldSet("deal"); got != tasks.CRMFieldSets["deal"] {
		t.Errorf("FormFieldSet(\"deal\") = %v, want %v", got, tasks.CRMFieldSets["deal"])
	}
	if got := FormFieldSet("lead"); got != tasks.CRMFieldSets["lead"] {
		t.Errorf("FormFieldSet(\"lead\") = %v, want %v", got, tasks.CRMFieldSets["lead"])
	}
	if got := FormFieldSet("other"); got != tasks.CRMFieldSets["lead"] {
		t.Errorf("FormFieldSet(\"other\") = %v, want default lead %v", got, tasks.CRMFieldSets["lead"])
	}
}

func TestFormLabeled_Unit(t *testing.T) {
	tr := func(key string) string {
		switch key {
		case "crm.form_field.name":
			return "Ваше имя"
		case "crm.form_field.phone":
			return "Номер телефона"
		default:
			return key
		}
	}

	form := Form{
		Fields: []FormField{
			{Key: "name", Label: ""},
			{Key: "phone", Label: "Контактный телефон"}, // explicit label preserved
			{Key: "f:99", Name: "Кастомное поле", Label: ""},
		},
	}

	form.Labeled(tr)

	if form.Fields[0].Label != "Ваше имя" {
		t.Errorf("Fields[0].Label = %q, want %q", form.Fields[0].Label, "Ваше имя")
	}
	if form.Fields[1].Label != "Контактный телефон" {
		t.Errorf("Fields[1].Label = %q, want %q", form.Fields[1].Label, "Контактный телефон")
	}
	if form.Fields[2].Label != "Кастомное поле" {
		t.Errorf("Fields[2].Label = %q, want %q", form.Fields[2].Label, "Кастомное поле")
	}
}

func TestFormToken_Unit(t *testing.T) {
	t1 := formToken()
	t2 := formToken()

	if len(t1) != 16 {
		t.Errorf("formToken length = %d, want 16", len(t1))
	}
	if t1 == t2 {
		t.Errorf("formTokens collided: %s and %s", t1, t2)
	}
	for _, r := range t1 {
		isBase64URL := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' || r == '_'
		if !isBase64URL {
			t.Errorf("formToken contains non-base64url char: %c", r)
		}
	}
}

func TestFormAnswer_Unit(t *testing.T) {
	form := Form{
		Fields: []FormField{
			{Key: "name", Label: "Имя", Required: true},
			{Key: "email", Label: "E-mail", Required: false},
			{Key: "phone", Label: "Телефон", Required: false},
			{Key: "comment", Label: "Комментарий", Required: false},
		},
		Consent: "Согласие на обработку данных",
	}

	// 1. Success with all fields and consent
	answers := map[string]string{
		"name":    "Иван",
		"email":   "ivan@example.com",
		"phone":   "+7 900 123-45-67",
		"comment": "Срочный\nзаказ",
	}
	ans, err := form.Answer(func(k string) string { return answers[k] }, true)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if len(ans) != 4 {
		t.Fatalf("expected 4 answers, got %d", len(ans))
	}

	// 2. Missing required field
	_, err = form.Answer(func(k string) string {
		if k == "name" {
			return "   "
		}
		return answers[k]
	}, true)
	if err == nil {
		t.Fatal("expected error for missing required name, got nil")
	}
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.crm_form_field_required" {
		t.Errorf("expected err.crm_form_field_required, got %v", err)
	}

	// 3. Invalid email format
	_, err = form.Answer(func(k string) string {
		if k == "email" {
			return "not-an-email"
		}
		return answers[k]
	}, true)
	if !errors.Is(err, ErrFormEmail) {
		t.Errorf("expected ErrFormEmail, got %v", err)
	}

	// 4. Missing required consent
	_, err = form.Answer(func(k string) string { return answers[k] }, false)
	if !errors.Is(err, ErrFormConsent) {
		t.Errorf("expected ErrFormConsent, got %v", err)
	}

	// 5. Completely empty answers
	emptyForm := Form{
		Fields: []FormField{
			{Key: "phone", Label: "Телефон", Required: false},
		},
	}
	_, err = emptyForm.Answer(func(k string) string { return "" }, true)
	if !errors.Is(err, ErrFormRequired) {
		t.Errorf("expected ErrFormRequired when all fields empty, got %v", err)
	}

	// 6. Non-comment newlines converted to spaces
	ansNewline, err := emptyForm.Answer(func(k string) string { return "123\n456" }, true)
	if err != nil || len(ansNewline) != 1 || ansNewline[0].Value != "123 456" {
		t.Errorf("expected non-comment newline replaced with space, got %+v (err: %v)", ansNewline, err)
	}
}

func TestFormText_Unit(t *testing.T) {
	ans := []FormAnswer{
		{Label: "Имя", Value: "Анна"},
		{Label: "Телефон", Value: "+7900"},
		{Label: "Пусто", Value: ""},
	}
	got := formText(ans)
	want := "Имя: Анна\nТелефон: +7900"
	if got != want {
		t.Errorf("formText() = %q, want %q", got, want)
	}

	if gotEmpty := formText(nil); gotEmpty != "" {
		t.Errorf("formText(nil) = %q, want empty", gotEmpty)
	}
}
