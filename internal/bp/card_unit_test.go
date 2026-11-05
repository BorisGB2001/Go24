package bp

import (
	"strings"
	"testing"
	"time"

	"go24/internal/crm"
)

// TestCardSetters: setting built-in fields on deal, lead, contact, and company cards.
func TestCardSetters(t *testing.T) {
	// dealCard
	var d crm.Deal
	cd := dealCard(&d)
	if err := cd.set("title", "New Deal"); err != nil || d.Title != "New Deal" {
		t.Errorf("deal title failed: %v", err)
	}
	if err := cd.set("amount", " 1 250,50 "); err != nil || d.Amount != 1250.50 {
		t.Errorf("deal amount failed: %v (amount=%f)", err, d.Amount)
	}
	if err := cd.set("amount", "invalid_num"); err == nil {
		t.Error("deal amount invalid number expected error")
	}
	if err := cd.set("close_date", "2026-11-15"); err != nil || d.CloseDate == nil || d.CloseDate.Format(time.DateOnly) != "2026-11-15" {
		t.Errorf("deal close_date failed: %v", err)
	}
	if err := cd.set("close_date", ""); err != nil || d.CloseDate != nil {
		t.Errorf("deal close_date empty should clear CloseDate: %v", err)
	}
	if err := cd.set("close_date", "not-a-date"); err == nil {
		t.Error("deal close_date invalid expected error")
	}
	if err := cd.set("responsible", "usr-1"); err != nil || d.Responsible.ID != "usr-1" {
		t.Errorf("deal responsible failed: %v", err)
	}
	if err := cd.set("comment", "important"); err != nil || d.Comment != "important" {
		t.Errorf("deal comment failed: %v", err)
	}
	if cd.own == nil {
		t.Error("dealCard own map should be initialized")
	}

	// leadCard
	var l crm.Lead
	cl := leadCard(&l)
	_ = cl.set("title", "Inquiry")
	_ = cl.set("name", "Bob")
	_ = cl.set("company_title", "Acme")
	_ = cl.set("phone", "+123456789")
	_ = cl.set("email", "bob@acme.test")
	_ = cl.set("amount", "5000")
	_ = cl.set("responsible", "usr-2")
	_ = cl.set("comment", "contact soon")
	if l.Title != "Inquiry" || l.Name != "Bob" || l.CompanyTitle != "Acme" || l.Phone != "+123456789" ||
		l.Email != "bob@acme.test" || l.Amount != 5000 || l.Responsible.ID != "usr-2" || l.Comment != "contact soon" {
		t.Errorf("leadCard field values mismatch: %+v", l)
	}
	if err := cl.set("amount", "bad"); err == nil {
		t.Error("leadCard bad amount expected error")
	}

	// contactCard
	var k crm.Contact
	ck := contactCard(&k)
	_ = ck.set("name", "Jane")
	_ = ck.set("position", "VP")
	_ = ck.set("phone", "987654")
	_ = ck.set("email", "jane@test.org")
	_ = ck.set("responsible", "usr-3")
	_ = ck.set("comment", "client note")
	if k.Name != "Jane" || k.Position != "VP" || k.Phone != "987654" || k.Email != "jane@test.org" ||
		k.Responsible.ID != "usr-3" || k.Comment != "client note" {
		t.Errorf("contactCard field values mismatch: %+v", k)
	}

	// companyCard
	var co crm.Company
	cco := companyCard(&co)
	_ = cco.set("title", "Globex")
	_ = cco.set("phone", "111222")
	_ = cco.set("email", "info@globex.test")
	_ = cco.set("website", "https://globex.test")
	_ = cco.set("address", "Springfield")
	_ = cco.set("responsible", "usr-4")
	_ = cco.set("comment", "enterprise")
	if co.Title != "Globex" || co.Phone != "111222" || co.Email != "info@globex.test" ||
		co.Website != "https://globex.test" || co.Address != "Springfield" || co.Responsible.ID != "usr-4" ||
		co.Comment != "enterprise" {
		t.Errorf("companyCard field values mismatch: %+v", co)
	}
}

// TestNumber: float parsing with spaces and commas.
func TestNumber(t *testing.T) {
	tests := []struct {
		input   string
		want    float64
		wantErr bool
	}{
		{"", 0, false},
		{"42", 42, false},
		{"-42", -42, false},
		{"3.14", 3.14, false},
		{"  1 000,50 ", 1000.50, false},
		{"-12,34", -12.34, false},
		{"bad_num", 0, true},
	}

	for _, tt := range tests {
		got, err := number(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("number(%q) err = %v, wantErr = %v", tt.input, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("number(%q) = %f, want %f", tt.input, got, tt.want)
		}
	}
}

// TestCheckCardStep: step validation for CRM, disk, and communication steps.
func TestCheckCardStep(t *testing.T) {
	tests := []struct {
		name    string
		entity  string
		step    Step
		wantErr string
	}{
		{
			name:   "valid deal update",
			entity: "deal",
			step: Step{
				Kind: "update",
				Sets: []Assign{{Field: "title", Value: "New Title"}},
			},
			wantErr: "",
		},
		{
			name:   "update on unsupported entity",
			entity: "none",
			step: Step{
				Kind: "update",
				Sets: []Assign{{Field: "title", Value: "New Title"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name:    "update with empty sets",
			entity:  "deal",
			step:    Step{Kind: "update", Sets: nil},
			wantErr: "err.bp_step",
		},
		{
			name:   "sets on disallowed kind",
			entity: "none",
			step: Step{
				Kind: "notify",
				Text: "Hello",
				Sets: []Assign{{Field: "x", Value: "1"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "set assign empty field",
			entity: "deal",
			step: Step{
				Kind: "update",
				Sets: []Assign{{Field: "", Value: "Val"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "set assign value too long",
			entity: "deal",
			step: Step{
				Kind: "update",
				Sets: []Assign{{Field: "title", Value: strings.Repeat("v", 1001)}},
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "post with subject too long",
			entity: "none",
			step: Step{
				Kind:    "post",
				Subject: strings.Repeat("s", 301),
				Text:    "Body",
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "activity with invalid deed",
			entity: "deal",
			step: Step{
				Kind: "activity",
				Deed: "unknown_deed",
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "activity with valid deed on deal",
			entity: "deal",
			step: Step{
				Kind: "activity",
				Deed: "call",
			},
			wantErr: "",
		},
		{
			name:   "activity deed on non-activity step",
			entity: "deal",
			step: Step{
				Kind: "notify",
				Text: "Notify",
				Deed: "call",
			},
			wantErr: "err.bp_step",
		},
		{
			name:    "document without doc template",
			entity:  "deal",
			step:    Step{Kind: "document"},
			wantErr: "err.bp_step",
		},
		{
			name:    "non-document step with doc template",
			entity:  "deal",
			step:    Step{Kind: "notify", Text: "Hi", Doc: "doc-1"},
			wantErr: "err.bp_step",
		},
		{
			name:   "crmconvert with invalid into entity",
			entity: "lead",
			step: Step{
				Kind: "crmconvert",
				Into: []string{"deal", "invalid_into"},
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "crmconvert with valid into on lead",
			entity: "lead",
			step: Step{
				Kind: "crmconvert",
				Into: []string{"deal", "contact"},
			},
			wantErr: "",
		},
		{
			name:   "diskfolder without drive",
			entity: "none",
			step: Step{
				Kind:   "diskfolder",
				Folder: "new-folder",
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "diskfolder with empty folder",
			entity: "none",
			step: Step{
				Kind:   "diskfolder",
				Drive:  "company",
				Folder: "  /  ",
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "diskfolder with invalid drive characters",
			entity: "none",
			step: Step{
				Kind:   "diskfolder",
				Drive:  "my/drive",
				Folder: "reports",
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "mail with empty subject",
			entity: "deal",
			step: Step{
				Kind:    "mail",
				Subject: "   ",
				To:      "client",
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "mail to client on non-CRM entity",
			entity: "none",
			step: Step{
				Kind:    "mail",
				Subject: "Letter",
				To:      "client",
			},
			wantErr: "err.bp_step",
		},
		{
			name:   "mail with empty To and empty Email",
			entity: "deal",
			step: Step{
				Kind:    "mail",
				Subject: "Letter",
				To:      "",
				Email:   "   ",
			},
			wantErr: "err.bp_step",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkCardStep(tt.entity, tt.step)
			gotKey := errKey(err)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && gotKey != tt.wantErr {
				t.Fatalf("errKey = %q (%v), want %q", gotKey, err, tt.wantErr)
			}
		})
	}
}

// TestCardFields: verification of predefined field sets for CRM entities.
func TestCardFields(t *testing.T) {
	for _, entity := range []string{"deal", "lead", "contact", "company"} {
		fields, ok := CardFields[entity]
		if !ok || len(fields) == 0 {
			t.Errorf("CardFields[%q] missing or empty", entity)
		}
		if !contains(fields, "responsible") || !contains(fields, "comment") {
			t.Errorf("CardFields[%q] should contain responsible and comment: %v", entity, fields)
		}
	}
}
