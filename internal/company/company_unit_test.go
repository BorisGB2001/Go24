package company

import (
	"strings"
	"testing"

	"go24/internal/i18n"
)

// TestDepartmentTotal: Total counts distinct employees across the department and all its sub-departments.
func TestDepartmentTotal(t *testing.T) {
	p1 := Person{ID: "u1", Name: "Alice"}
	p2 := Person{ID: "u2", Name: "Bob"}
	p3 := Person{ID: "u3", Name: "Charlie"}

	// Leaf with no employees
	leafEmpty := &Department{ID: "d4", Name: "Leaf Empty"}
	if got := leafEmpty.Total(); got != 0 {
		t.Errorf("empty leaf total = %d, want 0", got)
	}

	// Leaf with employees
	leaf1 := &Department{ID: "d2", Name: "Dev", Employees: []Person{p1, p2}}
	leaf2 := &Department{ID: "d3", Name: "QA", Employees: []Person{p2, p3}}

	// Root department with its own employee (duplicate with child) and sub-departments
	root := &Department{
		ID:        "d1",
		Name:      "Tech",
		Employees: []Person{p1},
		Children:  []*Department{leaf1, leaf2, leafEmpty},
	}

	// u1 is in root and leaf1; u2 is in leaf1 and leaf2; u3 is in leaf2 -> 3 unique persons total.
	if got := root.Total(); got != 3 {
		t.Errorf("root.Total() = %d, want 3", got)
	}
	if got := leaf1.Total(); got != 2 {
		t.Errorf("leaf1.Total() = %d, want 2", got)
	}
}

// TestCleanName: cleanName trims spaces and validates department name length (1–200 runes).
func TestCleanName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"valid standard", "Engineering", "Engineering", nil},
		{"valid with spaces", "   Sales & Marketing   ", "Sales & Marketing", nil},
		{"valid cyrillic", "Бухгалтерия и финансы", "Бухгалтерия и финансы", nil},
		{"empty string", "", "", i18n.E("err.department_name")},
		{"whitespace only", "   \t\n  ", "", i18n.E("err.department_name")},
		{"max runes 200", strings.Repeat("я", 200), strings.Repeat("я", 200), nil},
		{"too long 201", strings.Repeat("я", 201), "", i18n.E("err.department_name")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanName(tc.input)
			if tc.wantErr != nil {
				if err == nil || err.Error() != tc.wantErr.Error() {
					t.Fatalf("cleanName(%q) err = %v, want %v", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("cleanName(%q) unexpected err: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("cleanName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestNullable: nullable returns nil for empty strings and the string value otherwise.
func TestNullable(t *testing.T) {
	if got := nullable(""); got != nil {
		t.Errorf("nullable(\"\") = %v, want nil", got)
	}
	if got := nullable("dept-123"); got != "dept-123" {
		t.Errorf("nullable(\"dept-123\") = %v, want %q", got, "dept-123")
	}
}

// TestPhotoURL: PhotoURL returns empty when key is empty, or a hash-versioned path.
func TestPhotoURL(t *testing.T) {
	if got := PhotoURL("u1", ""); got != "" {
		t.Errorf("PhotoURL with empty key = %q, want empty", got)
	}
	u1 := PhotoURL("u1", "key1")
	u2 := PhotoURL("u1", "key1")
	if u1 != u2 {
		t.Errorf("PhotoURL is not deterministic: %q vs %q", u1, u2)
	}
	if !strings.HasPrefix(u1, "/u/u1/photo?v=") {
		t.Errorf("PhotoURL malformed prefix: %q", u1)
	}
	uDiff := PhotoURL("u1", "key2")
	if u1 == uDiff {
		t.Errorf("PhotoURL should differ for different keys: %q vs %q", u1, uDiff)
	}

	p := Person{ID: "u10", PhotoKey: "abc"}
	if p.PhotoURL() != PhotoURL("u10", "abc") {
		t.Errorf("Person.PhotoURL() mismatch")
	}

	prof := Profile{ID: "u20", PhotoKey: "def"}
	if prof.PhotoURL() != PhotoURL("u20", "def") {
		t.Errorf("Profile.PhotoURL() mismatch")
	}

	dept := Department{HeadID: "u30", HeadPhoto: "ghi"}
	if dept.HeadPhotoURL() != PhotoURL("u30", "ghi") {
		t.Errorf("Department.HeadPhotoURL() mismatch")
	}
}

// TestProfileNames: EditFirst, EditLast and FullName handle parts or split Name.
func TestProfileNames(t *testing.T) {
	cases := []struct {
		name      string
		p         Profile
		wantFirst string
		wantLast  string
		wantFull  string
	}{
		{
			name:      "explicit parts",
			p:         Profile{FirstName: "Анна", LastName: "Иванова", SecondName: "Петровна", Name: "Иванова Анна"},
			wantFirst: "Анна",
			wantLast:  "Иванова",
			wantFull:  "Иванова Анна Петровна",
		},
		{
			name:      "first name only",
			p:         Profile{FirstName: "Анна", Name: "Анна"},
			wantFirst: "Анна",
			wantLast:  "",
			wantFull:  "Анна",
		},
		{
			name:      "last name only",
			p:         Profile{LastName: "Иванова", Name: "Иванова"},
			wantFirst: "",
			wantLast:  "Иванова",
			wantFull:  "Иванова",
		},
		{
			name:      "split two-word name",
			p:         Profile{Name: "  Иван   Смирнов  "},
			wantFirst: "Иван",
			wantLast:  "Смирнов",
			wantFull:  "  Иван   Смирнов  ",
		},
		{
			name:      "split single word name",
			p:         Profile{Name: "Admin"},
			wantFirst: "Admin",
			wantLast:  "",
			wantFull:  "Admin",
		},
		{
			name:      "split three-word name",
			p:         Profile{Name: "Иван Иванович Иванов"},
			wantFirst: "Иван",
			wantLast:  "Иванович Иванов",
			wantFull:  "Иван Иванович Иванов",
		},
		{
			name:      "empty profile",
			p:         Profile{},
			wantFirst: "",
			wantLast:  "",
			wantFull:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.EditFirst(); got != tc.wantFirst {
				t.Errorf("EditFirst() = %q, want %q", got, tc.wantFirst)
			}
			if got := tc.p.EditLast(); got != tc.wantLast {
				t.Errorf("EditLast() = %q, want %q", got, tc.wantLast)
			}
			if got := tc.p.FullName(); got != tc.wantFull {
				t.Errorf("FullName() = %q, want %q", got, tc.wantFull)
			}
		})
	}
}

// TestProfileLinks: TelegramLink and WhatsAppLink formatting.
func TestProfileLinks(t *testing.T) {
	cases := []struct {
		name   string
		p      Profile
		wantTG string
		wantWA string
	}{
		{
			name:   "empty",
			p:      Profile{},
			wantTG: "",
			wantWA: "",
		},
		{
			name:   "telegram with @ and phone with symbols",
			p:      Profile{Telegram: "@alice_test", WhatsApp: "+7 (999) 123-45-67"},
			wantTG: "https://t.me/alice_test",
			wantWA: "https://wa.me/79991234567",
		},
		{
			name:   "telegram without @",
			p:      Profile{Telegram: "bob_dev"},
			wantTG: "https://t.me/bob_dev",
			wantWA: "",
		},
		{
			name:   "whatsapp with plain digits",
			p:      Profile{WhatsApp: "88005553535"},
			wantTG: "",
			wantWA: "https://wa.me/88005553535",
		},
		{
			name:   "whatsapp with no digits",
			p:      Profile{WhatsApp: "not-a-number"},
			wantTG: "",
			wantWA: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.TelegramLink(); got != tc.wantTG {
				t.Errorf("TelegramLink() = %q, want %q", got, tc.wantTG)
			}
			if got := tc.p.WhatsAppLink(); got != tc.wantWA {
				t.Errorf("WhatsAppLink() = %q, want %q", got, tc.wantWA)
			}
		})
	}
}

// TestCompanyRegexes: phoneRe, innerRe, telegramRe validation rules.
func TestCompanyRegexes(t *testing.T) {
	phoneTests := []struct {
		input string
		want  bool
	}{
		{"+7 (495) 123-45-67", true},
		{"+1-800-555-0199", true},
		{"88005553535", true},
		{"12345", true},
		{"+12345", true},
		{"1234", false},                // too short
		{"+", false},                   // no digits
		{"phone123", false},            // letters
		{"+7 (495) 123-45-67-", false}, // trailing dash
		{"-7 (495) 123-45-67", false},  // leading dash
	}
	for _, tc := range phoneTests {
		if got := phoneRe.MatchString(tc.input); got != tc.want {
			t.Errorf("phoneRe.MatchString(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}

	innerTests := []struct {
		input string
		want  bool
	}{
		{"1", true},
		{"101", true},
		{"12345678", true},
		{"", false},
		{"123456789", false}, // > 8 digits
		{"10a", false},
	}
	for _, tc := range innerTests {
		if got := innerRe.MatchString(tc.input); got != tc.want {
			t.Errorf("innerRe.MatchString(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}

	telegramTests := []struct {
		input string
		want  bool
	}{
		{"@alice", true},
		{"@alice_123", true},
		{"@A_b_c_d_e", true},
		{"alice", false},                       // missing @
		{"@al", false},                         // < 5 chars after @
		{"@1alice", false},                     // starts with digit
		{"@alice-bob", false},                  // dash not allowed
		{"@" + strings.Repeat("a", 32), true},  // 32 chars
		{"@" + strings.Repeat("a", 33), false}, // 33 chars (> 32)
	}
	for _, tc := range telegramTests {
		if got := telegramRe.MatchString(tc.input); got != tc.want {
			t.Errorf("telegramRe.MatchString(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
