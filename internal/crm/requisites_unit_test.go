package crm

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestRequisiteHasBank_Unit(t *testing.T) {
	if (Requisite{}).HasBank() {
		t.Error("expected empty Requisite to report HasBank() = false")
	}
	if !(Requisite{Bank: "Сбербанк"}).HasBank() {
		t.Error("expected Bank to make HasBank() = true")
	}
	if !(Requisite{BIK: "044525225"}).HasBank() {
		t.Error("expected BIK to make HasBank() = true")
	}
	if !(Requisite{Account: "40702810000000000001"}).HasBank() {
		t.Error("expected Account to make HasBank() = true")
	}
	if !(Requisite{CorrAccount: "30101810400000000225"}).HasBank() {
		t.Error("expected CorrAccount to make HasBank() = true")
	}
}

func TestDigits_Unit(t *testing.T) {
	tests := []struct {
		name string
		v    string
		n    []int
		want bool
	}{
		{"empty string is always valid", "", []int{10}, true},
		{"valid single allowed length", "1234567890", []int{10}, true},
		{"valid multiple allowed lengths", "123456789012", []int{10, 12}, true},
		{"wrong length", "12345", []int{10}, false},
		{"non-digits included", "12345abcde", []int{10}, false},
		{"hyphens not allowed in raw digits helper", "12-34-56", []int{6}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := digits(tt.v, tt.n...); got != tt.want {
				t.Errorf("digits(%q, %v) = %v, want %v", tt.v, tt.n, got, tt.want)
			}
		})
	}
}

func TestRequisiteCheck_Unit(t *testing.T) {
	validOrg := Requisite{
		Preset:      "org",
		Name:        "ООO Ромашка",
		INN:         "7701234567",
		KPP:         "770101001",
		OGRN:        "1027700132195",
		BIK:         "044525225",
		Account:     "40702810000000000001",
		CorrAccount: "30101810400000000225",
	}

	// 1. Valid organization
	q := validOrg
	if err := q.check(); err != nil {
		t.Fatalf("valid org check failed: %v", err)
	}

	// 2. Spaces and dashes stripped
	qWithSpaces := validOrg
	qWithSpaces.INN = "7701-234-567"
	qWithSpaces.Account = "40702 810 0 0000 0000001"
	qWithSpaces.CorrAccount = "30101-810-4-0000-0000225"
	if err := qWithSpaces.check(); err != nil {
		t.Fatalf("check with dashes/spaces failed: %v", err)
	}
	if qWithSpaces.INN != "7701234567" || qWithSpaces.Account != "40702810000000000001" {
		t.Errorf("spaces/dashes were not cleaned properly: INN=%q, Account=%q", qWithSpaces.INN, qWithSpaces.Account)
	}

	// 3. Valid IP (INN 12 digits, OGRN 15 digits, no KPP)
	validIP := Requisite{
		Preset: "ip",
		Name:   "ИП Иванов Иван Иванович",
		INN:    "770123456789",
		OGRN:   "304770000000015",
	}
	if err := validIP.check(); err != nil {
		t.Fatalf("valid IP check failed: %v", err)
	}

	// 4. IP with KPP should fail
	ipWithKPP := validIP
	ipWithKPP.KPP = "770101001"
	if err := ipWithKPP.check(); err == nil {
		t.Fatal("expected error for IP with KPP, got nil")
	} else {
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.crm_req_kpp" {
			t.Errorf("expected err.crm_req_kpp, got %v", err)
		}
	}

	// 5. Valid Person (INN 12 digits, no OGRN, no KPP)
	validPerson := Requisite{
		Preset: "person",
		Name:   "Петров Петр",
		INN:    "770123456789",
	}
	if err := validPerson.check(); err != nil {
		t.Fatalf("valid person check failed: %v", err)
	}

	// 6. Unknown preset returns err.bad_form
	qBadPreset := validOrg
	qBadPreset.Preset = "unknown"
	if err := qBadPreset.check(); err == nil {
		t.Fatal("expected error on unknown preset, got nil")
	} else {
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.bad_form" {
			t.Errorf("expected err.bad_form, got %v", err)
		}
	}

	// 7. Missing Name returns err.crm_req_name
	qNoName := validOrg
	qNoName.Name = "   "
	if err := qNoName.check(); err == nil {
		t.Fatal("expected error on missing name, got nil")
	} else {
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.crm_req_name" {
			t.Errorf("expected err.crm_req_name, got %v", err)
		}
	}

	// 8. Field exceeding 300 runes returns err.crm_field
	qLong := validOrg
	qLong.Address = strings.Repeat("a", 305)
	if err := qLong.check(); err == nil {
		t.Fatal("expected error on field > 300 runes, got nil")
	} else {
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.crm_field" {
			t.Errorf("expected err.crm_field, got %v", err)
		}
	}

	// 9. Wrong INN length for org (e.g. 11 digits instead of 10)
	qBadINN := validOrg
	qBadINN.INN = "77012345678"
	if err := qBadINN.check(); err == nil {
		t.Fatal("expected error on wrong INN length, got nil")
	} else {
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.crm_req_inn" {
			t.Errorf("expected err.crm_req_inn, got %v", err)
		}
	}

	// 10. Wrong BIK (8 digits instead of 9)
	qBadBIK := validOrg
	qBadBIK.BIK = "04452522"
	if err := qBadBIK.check(); err == nil {
		t.Fatal("expected error on wrong BIK, got nil")
	} else {
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.crm_req_bik" {
			t.Errorf("expected err.crm_req_bik, got %v", err)
		}
	}

	// 11. Wrong Account (19 digits instead of 20)
	qBadAcc := validOrg
	qBadAcc.Account = "4070281000000000001"
	if err := qBadAcc.check(); err == nil {
		t.Fatal("expected error on wrong Account, got nil")
	} else {
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.crm_req_account" {
			t.Errorf("expected err.crm_req_account, got %v", err)
		}
	}
}
