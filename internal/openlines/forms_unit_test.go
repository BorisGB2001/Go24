package openlines

import (
	"testing"

	"go24/internal/crm"
)

func TestFormTitle(t *testing.T) {
	cases := []struct {
		form crm.Form
		want string
	}{
		{crm.Form{Title: "Заявка на консультацию", Name: "lead_form"}, "Заявка на консультацию"},
		{crm.Form{Title: "", Name: "lead_form"}, "lead_form"},
		{crm.Form{Title: "", Name: ""}, ""},
	}
	for _, tc := range cases {
		if got := formTitle(tc.form); got != tc.want {
			t.Errorf("formTitle(%+v) = %q; want %q", tc.form, got, tc.want)
		}
	}
}
