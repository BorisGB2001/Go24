package sign

import (
	"testing"
)

func TestSelfTemplateStruct(t *testing.T) {
	st := SelfTemplate{
		TemplateID: "tpl-1",
		Name:       "Application for Vacation",
		SignerID:   "u-1",
		Signer:     "Director Ivan",
	}
	if st.TemplateID != "tpl-1" || st.Name != "Application for Vacation" ||
		st.SignerID != "u-1" || st.Signer != "Director Ivan" {
		t.Errorf("SelfTemplate struct fields mismatch: %+v", st)
	}
}
