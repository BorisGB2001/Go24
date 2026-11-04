package calendar

import "testing"

func TestCRMRefValidAndPath(t *testing.T) {
	validUUID := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"

	cases := []struct {
		ref       CRMRef
		wantValid bool
		wantPath  string
	}{
		{
			ref:       CRMRef{Entity: "deal", ID: validUUID},
			wantValid: true,
			wantPath:  "/crm/deals/" + validUUID,
		},
		{
			ref:       CRMRef{Entity: "lead", ID: validUUID},
			wantValid: true,
			wantPath:  "/crm/leads/" + validUUID,
		},
		{
			ref:       CRMRef{Entity: "contact", ID: validUUID},
			wantValid: true,
			wantPath:  "/crm/contacts/" + validUUID,
		},
		{
			ref:       CRMRef{Entity: "company", ID: validUUID},
			wantValid: true,
			wantPath:  "/crm/companies/" + validUUID,
		},
		{
			ref:       CRMRef{Entity: "invoice", ID: validUUID},
			wantValid: false,
		},
		{
			ref:       CRMRef{Entity: "deal", ID: "not-a-uuid"},
			wantValid: false,
		},
		{
			ref:       CRMRef{Entity: "", ID: validUUID},
			wantValid: false,
		},
	}

	for _, tc := range cases {
		if got := tc.ref.Valid(); got != tc.wantValid {
			t.Errorf("CRMRef{%s, %s}.Valid() = %v, want %v", tc.ref.Entity, tc.ref.ID, got, tc.wantValid)
		}
		if tc.wantValid {
			if got := tc.ref.Path(); got != tc.wantPath {
				t.Errorf("CRMRef{%s, %s}.Path() = %q, want %q", tc.ref.Entity, tc.ref.ID, got, tc.wantPath)
			}
		}
	}
}
