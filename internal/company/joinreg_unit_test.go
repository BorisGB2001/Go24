package company

import (
	"slices"
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
)

// TestCleanDomainsAndValidDomain: CleanDomains parses domain lists and validDomain verifies syntax.
func TestCleanDomainsAndValidDomain(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    []string
		wantErr error
	}{
		{
			name:  "standard comma and newline separated with @ and case",
			input: "@example.COM, @CORP.NET;\tsub.domain.org\nexample.com",
			want:  []string{"example.com", "corp.net", "sub.domain.org"},
		},
		{
			name:    "invalid domain with no dot",
			input:   "nodotdomain",
			wantErr: i18n.E("err.staff_domain", "nodotdomain"),
		},
		{
			name:    "invalid domain with trailing dot",
			input:   "example.com.",
			wantErr: i18n.E("err.staff_domain", "example.com."),
		},
		{
			name:    "invalid domain with leading dot",
			input:   ".example.com",
			wantErr: i18n.E("err.staff_domain", ".example.com"),
		},
		{
			name: "too many domains > 50",
			input: func() string {
				var list []string
				for i := range 51 {
					list = append(list, "d"+string(rune('a'+i%26))+string(rune('a'+i/26))+".com")
				}
				return strings.Join(list, " ")
			}(),
			wantErr: i18n.E("err.staff_domains"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CleanDomains(tc.input)
			if tc.wantErr != nil {
				if err == nil || err.Error() != tc.wantErr.Error() {
					t.Fatalf("CleanDomains() err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CleanDomains() unexpected err = %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("CleanDomains() = %v, want %v", got, tc.want)
			}
		})
	}

	validTests := []struct {
		domain string
		want   bool
	}{
		{"example.com", true},
		{"sub.domain.co.uk", true},
		{"123.456.net", true},
		{"xn--p1ai.com", true}, // IDN punycode
		{"nodot", false},
		{".leading.dot", false},
		{"trailing.dot.", false},
		{strings.Repeat("a", 254) + ".com", false}, // > 253 chars
		{"space in.com", false},
	}
	for _, vt := range validTests {
		if got := validDomain(vt.domain); got != vt.want {
			t.Errorf("validDomain(%q) = %v, want %v", vt.domain, got, vt.want)
		}
	}
}

// TestStaffSettingsRules: JoinOpen and DomainAllowed rules.
func TestStaffSettingsRules(t *testing.T) {
	token := newToken()
	if token == "" {
		t.Errorf("newToken() returned empty string")
	}

	set := StaffSettings{
		Register: true,
		Token:    token,
		Domains:  []string{"company.com", "partner.org"},
	}

	// JoinOpen
	if !set.JoinOpen(token) {
		t.Errorf("JoinOpen with correct token should be true")
	}
	if set.JoinOpen("wrong-token") {
		t.Errorf("JoinOpen with wrong token should be false")
	}

	setDisabled := set
	setDisabled.Register = false
	if setDisabled.JoinOpen(token) {
		t.Errorf("JoinOpen with Register=false should be false")
	}

	// DomainAllowed
	if !set.DomainAllowed("alice@company.com") {
		t.Errorf("alice@company.com should be allowed")
	}
	if !set.DomainAllowed("BOB@PARTNER.ORG") {
		t.Errorf("case-insensitive domain should be allowed")
	}
	if set.DomainAllowed("eve@gmail.com") {
		t.Errorf("eve@gmail.com should not be allowed")
	}

	// Any domain allowed when Domains list is empty
	setAny := StaffSettings{Domains: nil}
	if !setAny.DomainAllowed("anyone@anywhere.com") {
		t.Errorf("empty domains should allow any email")
	}
}

// TestInviteStateAndSplitInvitee: Invite.State lifecycle and splitInvitee parsing.
func TestInviteStateAndSplitInvitee(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	ptr := func(t time.Time) *time.Time { return &t }

	// Joined
	iJoined := Invite{
		Expires: now.AddDate(0, 0, 1),
		Joined:  true,
	}
	if got := iJoined.State(now); got != "joined" {
		t.Errorf("joined invite state = %q, want joined", got)
	}

	// Revoked
	iRevoked := Invite{
		Expires: now.AddDate(0, 0, 1),
		Revoked: ptr(now.Add(-time.Hour)),
	}
	if got := iRevoked.State(now); got != "revoked" {
		t.Errorf("revoked invite state = %q, want revoked", got)
	}

	// Expired
	iExpired := Invite{
		Expires: now.Add(-time.Hour),
	}
	if got := iExpired.State(now); got != "expired" {
		t.Errorf("expired invite state = %q, want expired", got)
	}

	// Waiting (pending)
	iWaiting := Invite{
		Expires: now.AddDate(0, 0, 3),
	}
	if got := iWaiting.State(now); got != "waiting" {
		t.Errorf("waiting invite state = %q, want waiting", got)
	}

	// splitInvitee
	splitCases := []struct {
		input     string
		wantEmail string
		wantName  string
	}{
		{"alice@example.com Alice Smith", "alice@example.com", "Alice Smith"},
		{"Alice Smith <alice@example.com>", "alice@example.com", "Alice Smith"},
		{"bob@example.com", "bob@example.com", ""},
		{"<charlie@example.com>", "charlie@example.com", ""},
		{"just a name without email", "", "just a name without email"},
	}
	for _, sc := range splitCases {
		email, name := splitInvitee(sc.input)
		if email != sc.wantEmail || name != sc.wantName {
			t.Errorf("splitInvitee(%q) = (%q, %q), want (%q, %q)",
				sc.input, email, name, sc.wantEmail, sc.wantName)
		}
	}
}
