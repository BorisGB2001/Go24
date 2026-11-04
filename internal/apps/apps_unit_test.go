package apps

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"go24/internal/apikeys"
)

// TestAppMethods verifies basic App helper methods.
func TestAppMethods(t *testing.T) {
	now := time.Now()
	aInstalled := App{InstalledAt: &now}
	aNotInstalled := App{InstalledAt: nil}

	if !aInstalled.Installed() {
		t.Error("aInstalled.Installed() = false, want true")
	}
	if aNotInstalled.Installed() {
		t.Error("aNotInstalled.Installed() = true, want false")
	}

	// Title fallback logic
	aWithMenu := App{Name: "System Name", MenuTitle: "Friendly Name"}
	aWithoutMenu := App{Name: "System Name", MenuTitle: ""}

	if got := aWithMenu.Title(); got != "Friendly Name" {
		t.Errorf("Title() = %q, want Friendly Name", got)
	}
	if got := aWithoutMenu.Title(); got != "System Name" {
		t.Errorf("Title() = %q, want System Name", got)
	}
}

// TestIsTokenPrefix verifies recognition of go24 access token prefixes.
func TestIsTokenPrefix(t *testing.T) {
	cases := []struct {
		token string
		want  bool
	}{
		{"go24at_123456789012_abcdef", true},
		{"go24at_", true},
		{"go24rt_123456789012_abcdef", false},
		{"go24_123456789012_abcdef", false},
		{"Bearer go24at_xxx", false},
		{"", false},
	}

	for _, c := range cases {
		if got := IsToken(c.token); got != c.want {
			t.Errorf("IsToken(%q) = %v, want %v", c.token, got, c.want)
		}
	}
}

// TestMemberIDCalculation verifies member_id formatting and hash stability.
func TestMemberIDCalculation(t *testing.T) {
	base1 := "https://crm.company.org"
	base2 := "https://crm.company.org/"

	id1 := MemberID(base1)
	id2 := MemberID(base2)

	if len(id1) != 32 {
		t.Fatalf("len(MemberID) = %d, want 32", len(id1))
	}
	if id1 != id2 {
		t.Fatalf("MemberID must normalize trailing slash: %q vs %q", id1, id2)
	}
	if MemberID("https://other.portal.org") == id1 {
		t.Error("different portal bases produced identical member_id")
	}
}

// TestAuthBlock verifies the auth dictionary structure returned to apps.
func TestAuthBlock(t *testing.T) {
	app := App{
		Scopes:           []string{"crm", "tasks"},
		ApplicationToken: "app_tok_xyz",
	}
	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	tokens := Tokens{
		Access:  "go24at_0123456789ab_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Refresh: "go24rt_0123456789ab_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Expires: expires,
		UserID:  "user-101",
	}

	base := "https://portal.example.com:8443/"
	auth := Auth(app, tokens, base)

	if auth["access_token"] != tokens.Access {
		t.Errorf("access_token = %v, want %q", auth["access_token"], tokens.Access)
	}
	if auth["refresh_token"] != tokens.Refresh {
		t.Errorf("refresh_token = %v, want %q", auth["refresh_token"], tokens.Refresh)
	}
	if auth["expires"] != expires.Unix() {
		t.Errorf("expires = %v, want %d", auth["expires"], expires.Unix())
	}
	if auth["scope"] != "crm,tasks" {
		t.Errorf("scope = %v, want crm,tasks", auth["scope"])
	}
	if auth["domain"] != "portal.example.com:8443" {
		t.Errorf("domain = %v, want portal.example.com:8443", auth["domain"])
	}
	if auth["client_endpoint"] != "https://portal.example.com:8443/api/v1/" {
		t.Errorf("client_endpoint = %v, want https://portal.example.com:8443/api/v1/", auth["client_endpoint"])
	}
	if auth["server_endpoint"] != "https://portal.example.com:8443/oauth/" {
		t.Errorf("server_endpoint = %v, want https://portal.example.com:8443/oauth/", auth["server_endpoint"])
	}
	if auth["member_id"] != MemberID(base) {
		t.Errorf("member_id = %v, want %q", auth["member_id"], MemberID(base))
	}
	if auth["user_id"] != tokens.UserID {
		t.Errorf("user_id = %v, want %q", auth["user_id"], tokens.UserID)
	}
	if auth["status"] != "L" {
		t.Errorf("status = %v, want L", auth["status"])
	}
	if auth["application_token"] != app.ApplicationToken {
		t.Errorf("application_token = %v, want %q", auth["application_token"], app.ApplicationToken)
	}
}

// TestParseTokenEdgeCases verifies token decomposition and format validation.
func TestParseTokenEdgeCases(t *testing.T) {
	goodID := strings.Repeat("a", 12)
	goodSec := strings.Repeat("b", 64)

	// Valid token
	id, sec, ok := parse(accessPrefix+goodID+"_"+goodSec, accessPrefix)
	if !ok || id != goodID || sec != goodSec {
		t.Fatalf("parse failed for valid token: ok=%v id=%q sec=%q", ok, id, sec)
	}

	invalidCases := []struct {
		name  string
		token string
	}{
		{"empty string", ""},
		{"wrong prefix", refreshPrefix + goodID + "_" + goodSec},
		{"no underscore", accessPrefix + goodID + goodSec},
		{"multiple underscores", accessPrefix + goodID + "_" + goodSec[:32] + "_" + goodSec[32:]},
		{"id too short", accessPrefix + goodID[:11] + "_" + goodSec},
		{"id too long", accessPrefix + goodID + "a_" + goodSec},
		{"secret too short", accessPrefix + goodID + "_" + goodSec[:63]},
		{"secret too long", accessPrefix + goodID + "_" + goodSec + "b"},
		{"id non-hex", accessPrefix + "zzzzzzzzzzzz_" + goodSec},
		{"secret non-hex", accessPrefix + goodID + "_" + strings.Repeat("z", 64)},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := parse(tc.token, accessPrefix); ok {
				t.Errorf("accepted invalid token %q", tc.token)
			}
		})
	}
}

// TestHashAndHex verifies cryptographic hashing and hex helper utilities.
func TestHashAndHex(t *testing.T) {
	secret := "my-secret-key-to-hash"
	expectedSum := sha256.Sum256([]byte(secret))
	gotHash := hash(secret)

	if len(gotHash) != 32 {
		t.Fatalf("len(hash) = %d, want 32", len(gotHash))
	}
	for i := range expectedSum {
		if gotHash[i] != expectedSum[i] {
			t.Fatalf("hash mismatch at byte %d", i)
		}
	}

	// isHex tests
	if !isHex("") {
		t.Error("isHex(\"\") = false, want true")
	}
	if !isHex("0123456789abcdefABCDEF") {
		t.Error("isHex(valid hex) = false, want true")
	}
	if isHex("abc") { // odd length
		t.Error("isHex(odd length) = true, want false")
	}
	if isHex("xyz123") {
		t.Error("isHex(invalid chars) = true, want false")
	}

	// randomHex tests
	r16 := randomHex(16)
	if len(r16) != 32 || !isHex(r16) {
		t.Errorf("randomHex(16) = %q (len %d)", r16, len(r16))
	}
	r16b := randomHex(16)
	if r16 == r16b {
		t.Error("consecutive randomHex calls returned identical string")
	}
}

// TestCheckURL verifies URL validation for application handlers.
func TestCheckURL(t *testing.T) {
	cases := []struct {
		url      string
		required bool
		wantErr  bool
	}{
		{"", false, false},
		{"", true, true},
		{"https://example.com/handler", true, false},
		{"http://localhost:3000/app", false, false},
		{"ftp://example.com", false, true},
		{"javascript:alert(1)", true, true},
		{"http://", true, true},
		{strings.Repeat("a", 1001), false, true},
	}

	for _, c := range cases {
		err := checkURL(c.url, c.required)
		if (err != nil) != c.wantErr {
			t.Errorf("checkURL(%q, %v) err = %v, wantErr = %v", c.url, c.required, err, c.wantErr)
		}
	}
}

// TestStoreWake verifies the non-blocking event wake mechanism.
func TestStoreWake(t *testing.T) {
	s := NewStore(nil)

	// Reading before wake should have empty channel
	select {
	case <-s.Woken():
		t.Fatal("unexpected wake signal before Wake() called")
	default:
	}

	// First wake puts item into channel
	s.Wake()
	select {
	case <-s.Woken():
		// Received as expected
	default:
		t.Fatal("expected wake signal")
	}

	// Multiple wakes do not block
	s.Wake()
	s.Wake()
	s.Wake()
}

// TestSavePreconditions verifies Save validation before issuing DB queries.
func TestSavePreconditions(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	tests := []struct {
		name       string
		app        App
		wantSubstr string
	}{
		{
			name:       "empty name",
			app:        App{Name: ""},
			wantSubstr: "err.app_name",
		},
		{
			name:       "name too long in runes",
			app:        App{Name: strings.Repeat("Б", 101)},
			wantSubstr: "err.app_name",
		},
		{
			name:       "menu title too long in runes",
			app:        App{Name: "Valid Name", MenuTitle: strings.Repeat("Ф", 101)},
			wantSubstr: "err.app_name",
		},
		{
			name:       "invalid handler URL for dynamic app",
			app:        App{Name: "Valid App", HandlerURL: "ftp://bad-url", Scopes: []string{"crm"}},
			wantSubstr: "err.app_url",
		},
		{
			name:       "invalid install URL",
			app:        App{Name: "Valid App", HandlerURL: "https://valid.com", InstallURL: ":::bad", Scopes: []string{"crm"}},
			wantSubstr: "err.app_url",
		},
		{
			name:       "empty scopes",
			app:        App{Name: "Valid App", HandlerURL: "https://valid.com", Scopes: []string{}},
			wantSubstr: "err.key_scopes_required",
		},
		{
			name:       "unknown scopes only",
			app:        App{Name: "Valid App", HandlerURL: "https://valid.com", Scopes: []string{"invalid_scope_1", "invalid_scope_2"}},
			wantSubstr: "err.key_scopes_required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Save(ctx, "user-1", tc.app)
			if err == nil {
				t.Fatalf("expected Save to fail, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not contain expected substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}

	// Static app clears handler URL and passes URL check without it
	staticApp := App{
		Name:    "Static App",
		Static:  true,
		Scopes:  []string{apikeys.AllScopes[0]},
		APIOnly: true, // will be reset by Save
	}
	// Calling Save will proceed past URL and scope checks to s.db, where it panics on nil db
	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected static app to reach database call and panic with nil db")
		}
	}()
	_, _ = s.Save(ctx, "user-1", staticApp)
}

// TestRefreshAndAuthenticateInvalidToken verifies pre-database rejection of malformed tokens.
func TestRefreshAndAuthenticateInvalidToken(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	app := App{ID: "app-1"}

	// Malformed refresh token
	_, err := s.Refresh(ctx, app, "invalid_refresh_token")
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Refresh(bad) = %v, want ErrInvalid", err)
	}

	// Malformed access token
	_, err = s.Authenticate(ctx, "invalid_access_token", "127.0.0.1")
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Authenticate(bad) = %v, want ErrInvalid", err)
	}
}
