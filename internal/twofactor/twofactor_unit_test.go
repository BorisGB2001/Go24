package twofactor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
)

// TestPolicy_For tests whether the second factor is required based on policy mode and admin status.
func TestPolicy_For(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		admin bool
		want  bool
	}{
		// Mode "": not required for anyone
		{name: "empty mode regular user", mode: "", admin: false, want: false},
		{name: "empty mode admin", mode: "", admin: true, want: false},

		// Mode "admins": required for administrators only
		{name: "admins mode regular user", mode: "admins", admin: false, want: false},
		{name: "admins mode admin", mode: "admins", admin: true, want: true},

		// Mode "all": required for every employee
		{name: "all mode regular user", mode: "all", admin: false, want: true},
		{name: "all mode admin", mode: "all", admin: true, want: true},

		// Unknown / invalid modes should not require 2FA
		{name: "unknown mode regular user", mode: "unknown", admin: false, want: false},
		{name: "unknown mode admin", mode: "unknown", admin: true, want: false},
		{name: "case sensitive all regular user", mode: "ALL", admin: false, want: false},
		{name: "case sensitive all admin", mode: "ALL", admin: true, want: false},
		{name: "case sensitive admins admin", mode: "Admins", admin: true, want: false},
		{name: "mode with whitespace admin", mode: "admins ", admin: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Policy{Mode: tt.mode}
			if got := p.For(tt.admin); got != tt.want {
				t.Errorf("Policy{Mode: %q}.For(%v) = %v, want %v", tt.mode, tt.admin, got, tt.want)
			}
		})
	}
}

// TestPolicy_Deadline tests deadline calculation counting from policy activation or joining date,
// whichever is later, plus the configured grace period in days.
func TestPolicy_Deadline(t *testing.T) {
	since := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		since  time.Time
		days   int
		joined time.Time
		want   time.Time
	}{
		{
			name:   "joined before policy activation counts from since",
			since:  since,
			days:   7,
			joined: since.AddDate(0, 0, -10),
			want:   since.Add(7 * 24 * time.Hour),
		},
		{
			name:   "joined after policy activation counts from joined",
			since:  since,
			days:   7,
			joined: since.AddDate(0, 0, 5),
			want:   since.AddDate(0, 0, 5).Add(7 * 24 * time.Hour),
		},
		{
			name:   "joined exactly at policy activation counts from since",
			since:  since,
			days:   7,
			joined: since,
			want:   since.Add(7 * 24 * time.Hour),
		},
		{
			name:   "zero days joined before since gives since",
			since:  since,
			days:   0,
			joined: since.AddDate(0, -1, 0),
			want:   since,
		},
		{
			name:   "zero days joined after since gives joined",
			since:  since,
			days:   0,
			joined: since.AddDate(0, 1, 0),
			want:   since.AddDate(0, 1, 0),
		},
		{
			name:   "one day grace period",
			since:  since,
			days:   1,
			joined: since.AddDate(0, 0, -1),
			want:   since.Add(24 * time.Hour),
		},
		{
			name:   "maximum allowed 365 days grace period",
			since:  since,
			days:   365,
			joined: since.AddDate(0, 0, -100),
			want:   since.Add(365 * 24 * time.Hour),
		},
		{
			name:   "nanosecond precision joined 1ns after since counts from joined",
			since:  since,
			days:   3,
			joined: since.Add(1 * time.Nanosecond),
			want:   since.Add(1 * time.Nanosecond).Add(3 * 24 * time.Hour),
		},
		{
			name:   "nanosecond precision joined 1ns before since counts from since",
			since:  since,
			days:   3,
			joined: since.Add(-1 * time.Nanosecond),
			want:   since.Add(3 * 24 * time.Hour),
		},
		{
			name:   "zero since time with valid joined counts from joined",
			since:  time.Time{},
			days:   5,
			joined: since,
			want:   since.Add(5 * 24 * time.Hour),
		},
		{
			name:   "zero joined time with valid since counts from since",
			since:  since,
			days:   5,
			joined: time.Time{},
			want:   since.Add(5 * 24 * time.Hour),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Policy{Since: tt.since, Days: tt.days}
			got := p.Deadline(tt.joined)
			if !got.Equal(tt.want) {
				t.Errorf("Deadline(%v) = %v, want %v", tt.joined, got, tt.want)
			}
		})
	}
}

// TestPolicy_Judge tests the sign-in judgment: Pass when factor is not required or was used;
// Remind when required, not used, and within grace period; Deny when grace period has expired.
func TestPolicy_Judge(t *testing.T) {
	since := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	joinedOld := since.AddDate(0, 0, -30)
	joinedNew := since.AddDate(0, 0, 2)

	pAdmins := Policy{Mode: "admins", Days: 7, Since: since}
	pAll := Policy{Mode: "all", Days: 7, Since: since}
	pNone := Policy{Mode: "", Days: 0, Since: since}
	pZeroDays := Policy{Mode: "all", Days: 0, Since: since}

	dueOld := since.Add(7 * 24 * time.Hour)
	dueNew := joinedNew.Add(7 * 24 * time.Hour)

	tests := []struct {
		name        string
		policy      Policy
		admin       bool
		mfa         bool
		joined      time.Time
		now         time.Time
		wantVerdict Verdict
		wantDue     time.Time
	}{
		// MFA used always passes regardless of requirements, deadlines, or role
		{
			name:        "mfa used passes for admin under admins policy even after deadline",
			policy:      pAdmins,
			admin:       true,
			mfa:         true,
			joined:      joinedOld,
			now:         dueOld.Add(24 * time.Hour),
			wantVerdict: Pass,
			wantDue:     time.Time{},
		},
		{
			name:        "mfa used passes for regular user under all policy even after deadline",
			policy:      pAll,
			admin:       false,
			mfa:         true,
			joined:      joinedOld,
			now:         dueOld.Add(48 * time.Hour),
			wantVerdict: Pass,
			wantDue:     time.Time{},
		},

		// Policy not required passes
		{
			name:        "empty policy passes for admin without mfa",
			policy:      pNone,
			admin:       true,
			mfa:         false,
			joined:      joinedOld,
			now:         since.Add(100 * 24 * time.Hour),
			wantVerdict: Pass,
			wantDue:     time.Time{},
		},
		{
			name:        "empty policy passes for regular user without mfa",
			policy:      pNone,
			admin:       false,
			mfa:         false,
			joined:      joinedOld,
			now:         since.Add(100 * 24 * time.Hour),
			wantVerdict: Pass,
			wantDue:     time.Time{},
		},
		{
			name:        "admins policy passes for regular user without mfa",
			policy:      pAdmins,
			admin:       false,
			mfa:         false,
			joined:      joinedOld,
			now:         dueOld.Add(24 * time.Hour),
			wantVerdict: Pass,
			wantDue:     time.Time{},
		},

		// Grace period active (now < due) -> Remind
		{
			name:        "admins policy reminds admin when now is well before due",
			policy:      pAdmins,
			admin:       true,
			mfa:         false,
			joined:      joinedOld,
			now:         dueOld.Add(-24 * time.Hour),
			wantVerdict: Remind,
			wantDue:     dueOld,
		},
		{
			name:        "all policy reminds regular employee when now is 1 nanosecond before due",
			policy:      pAll,
			admin:       false,
			mfa:         false,
			joined:      joinedOld,
			now:         dueOld.Add(-1 * time.Nanosecond),
			wantVerdict: Remind,
			wantDue:     dueOld,
		},
		{
			name:        "joined after policy reminds based on joined deadline",
			policy:      pAll,
			admin:       false,
			mfa:         false,
			joined:      joinedNew,
			now:         dueNew.Add(-1 * time.Hour),
			wantVerdict: Remind,
			wantDue:     dueNew,
		},

		// Grace period boundary (now == due) -> Deny (since now.Before(due) is false)
		{
			name:        "exact deadline moment denies sign-in",
			policy:      pAdmins,
			admin:       true,
			mfa:         false,
			joined:      joinedOld,
			now:         dueOld,
			wantVerdict: Deny,
			wantDue:     dueOld,
		},

		// Grace period expired (now > due) -> Deny
		{
			name:        "one nanosecond after deadline denies sign-in",
			policy:      pAll,
			admin:       true,
			mfa:         false,
			joined:      joinedOld,
			now:         dueOld.Add(1 * time.Nanosecond),
			wantVerdict: Deny,
			wantDue:     dueOld,
		},
		{
			name:        "well past deadline denies sign-in",
			policy:      pAll,
			admin:       false,
			mfa:         false,
			joined:      joinedOld,
			now:         dueOld.Add(30 * 24 * time.Hour),
			wantVerdict: Deny,
			wantDue:     dueOld,
		},

		// Zero days grace period (Days == 0): immediate enforcement
		{
			name:        "zero days policy denies sign-in when now is equal to policy start",
			policy:      pZeroDays,
			admin:       true,
			mfa:         false,
			joined:      joinedOld,
			now:         since,
			wantVerdict: Deny,
			wantDue:     since,
		},
		{
			name:        "zero days policy denies sign-in when now is after policy start",
			policy:      pZeroDays,
			admin:       false,
			mfa:         false,
			joined:      joinedOld,
			now:         since.Add(1 * time.Hour),
			wantVerdict: Deny,
			wantDue:     since,
		},
		{
			name:        "zero days policy reminds if now is before policy start (clock skew)",
			policy:      pZeroDays,
			admin:       true,
			mfa:         false,
			joined:      joinedOld,
			now:         since.Add(-1 * time.Minute),
			wantVerdict: Remind,
			wantDue:     since,
		},
		{
			name:        "zero days policy for newly joined employee denies at joined time",
			policy:      pZeroDays,
			admin:       false,
			mfa:         false,
			joined:      joinedNew,
			now:         joinedNew,
			wantVerdict: Deny,
			wantDue:     joinedNew,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVerdict, gotDue := tt.policy.Judge(tt.admin, tt.mfa, tt.joined, tt.now)
			if gotVerdict != tt.wantVerdict {
				t.Errorf("Judge verdict = %v, want %v", gotVerdict, tt.wantVerdict)
			}
			if !gotDue.Equal(tt.wantDue) {
				t.Errorf("Judge due = %v, want %v", gotDue, tt.wantDue)
			}
		})
	}
}

// TestMFA tests detection of second factor authentication from ID token amr claims.
func TestMFA(t *testing.T) {
	tests := []struct {
		name string
		amr  []string
		want bool
	}{
		{name: "nil slice", amr: nil, want: false},
		{name: "empty slice", amr: []string{}, want: false},
		{name: "only password", amr: []string{"pwd"}, want: false},
		{name: "only mfa", amr: []string{"mfa"}, want: true},
		{name: "pwd and mfa", amr: []string{"pwd", "mfa"}, want: true},
		{name: "mfa first then pwd", amr: []string{"mfa", "pwd"}, want: true},
		{name: "middle of multi-factor list", amr: []string{"pwd", "otp", "mfa", "hwk"}, want: true},
		{name: "duplicate mfa entries", amr: []string{"mfa", "mfa"}, want: true},
		{name: "case sensitive uppercase MFA is not recognized", amr: []string{"MFA"}, want: false},
		{name: "case sensitive mixed case Mfa is not recognized", amr: []string{"Mfa"}, want: false},
		{name: "mfa with leading space is not recognized", amr: []string{" mfa"}, want: false},
		{name: "mfa with trailing space is not recognized", amr: []string{"mfa "}, want: false},
		{name: "partial match totp_mfa is not recognized", amr: []string{"totp_mfa"}, want: false},
		{name: "totp alone without mfa claim is not recognized", amr: []string{"totp"}, want: false},
		{name: "webauthn alone without mfa claim is not recognized", amr: []string{"webauthn"}, want: false},
		{name: "empty string element", amr: []string{""}, want: false},
		{name: "empty string and mfa", amr: []string{"", "mfa"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MFA(tt.amr); got != tt.want {
				t.Errorf("MFA(%v) = %v, want %v", tt.amr, got, tt.want)
			}
		})
	}
}

// TestModes_And_DaysChoice tests the supported policy modes and available grace period days.
func TestModes_And_DaysChoice(t *testing.T) {
	wantModes := []string{"", "admins", "all"}
	if !slices.Equal(Modes, wantModes) {
		t.Errorf("Modes = %v, want %v", Modes, wantModes)
	}

	wantDays := []int{0, 1, 3, 7, 14, 30}
	if !slices.Equal(DaysChoice, wantDays) {
		t.Errorf("DaysChoice = %v, want %v", DaysChoice, wantDays)
	}

	for i := 1; i < len(DaysChoice); i++ {
		if DaysChoice[i] <= DaysChoice[i-1] {
			t.Errorf("DaysChoice is not strictly ascending: %v", DaysChoice)
		}
	}
	for _, d := range DaysChoice {
		if d < 0 || d > 365 {
			t.Errorf("DaysChoice contains out-of-range value: %d", d)
		}
	}
}

// TestVerdict_Values tests verdict constants ordering and uniqueness.
func TestVerdict_Values(t *testing.T) {
	if Pass != 0 {
		t.Errorf("Pass = %d, want 0", Pass)
	}
	if Remind != 1 {
		t.Errorf("Remind = %d, want 1", Remind)
	}
	if Deny != 2 {
		t.Errorf("Deny = %d, want 2", Deny)
	}
	if Pass == Remind || Remind == Deny || Pass == Deny {
		t.Errorf("Verdict constants are not mutually distinct")
	}
}

// TestStore_Save_Validation tests input validation of policy mode and days in Store.Save before database access.
func TestStore_Save_Validation(t *testing.T) {
	store := NewStore(nil)
	ctx := context.Background()

	tests := []struct {
		name       string
		policy     Policy
		wantErrKey string
	}{
		// Mode validation
		{
			name:       "invalid mode unknown",
			policy:     Policy{Mode: "superadmin", Days: 7},
			wantErrKey: "twofa.err_mode",
		},
		{
			name:       "invalid mode uppercase ADMINS",
			policy:     Policy{Mode: "ADMINS", Days: 7},
			wantErrKey: "twofa.err_mode",
		},
		{
			name:       "invalid mode uppercase ALL",
			policy:     Policy{Mode: "ALL", Days: 7},
			wantErrKey: "twofa.err_mode",
		},
		{
			name:       "invalid mode single space",
			policy:     Policy{Mode: " ", Days: 7},
			wantErrKey: "twofa.err_mode",
		},
		{
			name:       "invalid mode none string",
			policy:     Policy{Mode: "none", Days: 7},
			wantErrKey: "twofa.err_mode",
		},

		// Days validation
		{
			name:       "negative days -1",
			policy:     Policy{Mode: "admins", Days: -1},
			wantErrKey: "twofa.err_days",
		},
		{
			name:       "negative days -100",
			policy:     Policy{Mode: "all", Days: -100},
			wantErrKey: "twofa.err_days",
		},
		{
			name:       "days exceed 365 max (366)",
			policy:     Policy{Mode: "admins", Days: 366},
			wantErrKey: "twofa.err_days",
		},
		{
			name:       "days exceed 365 max (1000)",
			policy:     Policy{Mode: "all", Days: 1000},
			wantErrKey: "twofa.err_days",
		},
		{
			name:       "negative days with empty mode fails days validation",
			policy:     Policy{Mode: "", Days: -5},
			wantErrKey: "twofa.err_days",
		},

		// Mode check precedes days check
		{
			name:       "both invalid mode and days returns mode error first",
			policy:     Policy{Mode: "invalid", Days: -10},
			wantErrKey: "twofa.err_mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed, err := store.Save(ctx, "actor-1", tt.policy)
			if changed {
				t.Errorf("Save() changed = true on validation failure, want false")
			}
			if err == nil {
				t.Fatalf("Save() expected error with key %q, got nil", tt.wantErrKey)
			}
			var ie *i18n.Error
			if !errors.As(err, &ie) {
				t.Fatalf("Save() error is not *i18n.Error: %v", err)
			}
			if ie.Key != tt.wantErrKey {
				t.Errorf("Save() error key = %q, want %q", ie.Key, tt.wantErrKey)
			}
		})
	}
}

// TestPolicy_JSON tests JSON marshaling and unmarshaling of Policy structs.
func TestPolicy_JSON(t *testing.T) {
	since := time.Date(2026, 10, 20, 14, 30, 0, 0, time.UTC)
	p := Policy{
		Mode:  "admins",
		Days:  14,
		Since: since,
	}

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal(Policy) error: %v", err)
	}

	jsonStr := string(raw)
	for _, key := range []string{`"mode":"admins"`, `"days":14`, `"since":"2026-10-20T14:30:00Z"`} {
		if !strings.Contains(jsonStr, key) {
			t.Errorf("marshaled JSON %s missing expected field %s", jsonStr, key)
		}
	}

	var decoded Policy
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(Policy) error: %v", err)
	}
	if decoded.Mode != p.Mode || decoded.Days != p.Days || !decoded.Since.Equal(p.Since) {
		t.Errorf("decoded Policy = %+v, want %+v", decoded, p)
	}

	// Empty JSON decoding
	var empty Policy
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil {
		t.Fatalf("unmarshal empty JSON error: %v", err)
	}
	if empty.Mode != "" || empty.Days != 0 || !empty.Since.IsZero() {
		t.Errorf("empty JSON gave %+v, want zero Policy", empty)
	}

	// Malformed JSON error
	var malformed Policy
	if err := json.Unmarshal([]byte(`{"mode":`), &malformed); err == nil {
		t.Errorf("unmarshal malformed JSON expected error, got nil")
	}
}

// TestPerson_JSON tests JSON serialization and omitempty behavior for Person records.
func TestPerson_JSON(t *testing.T) {
	mfaAt := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	deadline := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

	// Case 1: Deadline present, MFAAt present
	p1 := Person{
		ID:       "usr-1",
		Name:     "Alice Smith",
		Admin:    true,
		Required: true,
		MFAAt:    &mfaAt,
		Deadline: &deadline,
	}

	raw1, err := json.Marshal(p1)
	if err != nil {
		t.Fatalf("Marshal(Person) error: %v", err)
	}
	s1 := string(raw1)
	if !strings.Contains(s1, `"deadline"`) {
		t.Errorf("expected deadline field in JSON: %s", s1)
	}
	if !strings.Contains(s1, `"mfa_at"`) {
		t.Errorf("expected mfa_at field in JSON: %s", s1)
	}

	var dec1 Person
	if err := json.Unmarshal(raw1, &dec1); err != nil {
		t.Fatalf("Unmarshal(Person) error: %v", err)
	}
	if dec1.ID != p1.ID || dec1.Name != p1.Name || dec1.Admin != p1.Admin || dec1.Required != p1.Required {
		t.Errorf("decoded Person basic fields mismatch: %+v", dec1)
	}
	if dec1.MFAAt == nil || !dec1.MFAAt.Equal(mfaAt) {
		t.Errorf("decoded MFAAt mismatch: %v", dec1.MFAAt)
	}
	if dec1.Deadline == nil || !dec1.Deadline.Equal(deadline) {
		t.Errorf("decoded Deadline mismatch: %v", dec1.Deadline)
	}

	// Case 2: Deadline nil (must be omitted), MFAAt nil (must serialize as null)
	p2 := Person{
		ID:       "usr-2",
		Name:     "Bob Jones",
		Admin:    false,
		Required: false,
		MFAAt:    nil,
		Deadline: nil,
	}

	raw2, err := json.Marshal(p2)
	if err != nil {
		t.Fatalf("Marshal(Person) error: %v", err)
	}
	s2 := string(raw2)
	if strings.Contains(s2, `"deadline"`) {
		t.Errorf("expected deadline field to be omitted when nil: %s", s2)
	}
	if !strings.Contains(s2, `"mfa_at":null`) {
		t.Errorf("expected mfa_at:null in JSON: %s", s2)
	}

	var dec2 Person
	if err := json.Unmarshal(raw2, &dec2); err != nil {
		t.Fatalf("Unmarshal(Person) error: %v", err)
	}
	if dec2.Deadline != nil {
		t.Errorf("expected nil Deadline, got %v", dec2.Deadline)
	}
	if dec2.MFAAt != nil {
		t.Errorf("expected nil MFAAt, got %v", dec2.MFAAt)
	}
}
