package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAdminActive: AdminActive reports true only when both Admin and AdminMode are true; Granted does not affect it.
func TestAdminActive(t *testing.T) {
	tests := []struct {
		name string
		sess Session
		want bool
	}{
		{name: "neither", sess: Session{Admin: false, AdminMode: false}, want: false},
		{name: "mode only", sess: Session{Admin: false, AdminMode: true}, want: false},
		{name: "admin only", sess: Session{Admin: true, AdminMode: false}, want: false},
		{name: "both", sess: Session{Admin: true, AdminMode: true}, want: true},
		{name: "granted with admin mode", sess: Session{Admin: false, AdminMode: true, Granted: true}, want: false},
		{name: "granted with admin and mode", sess: Session{Admin: true, AdminMode: true, Granted: true}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sess.AdminActive(); got != tt.want {
				t.Fatalf("AdminActive() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSessions_NewAndTTL: constructor initialises secret, secure flag, and default 12-hour TTL.
func TestSessions_NewAndTTL(t *testing.T) {
	s1 := NewSessions("test-secret", true)
	if string(s1.secret) != "test-secret" {
		t.Fatalf("secret = %q, want test-secret", string(s1.secret))
	}
	if !s1.secure {
		t.Fatal("expected secure = true")
	}
	if s1.TTL() != 12*time.Hour {
		t.Fatalf("TTL = %v, want 12h", s1.TTL())
	}

	s2 := NewSessions("other-secret", false)
	if s2.secure {
		t.Fatal("expected secure = false")
	}
	if s2.TTL() != 12*time.Hour {
		t.Fatalf("TTL = %v, want 12h", s2.TTL())
	}
}

// TestSessions_Set: sets cookie with expected name, path, flags, and expiration, and clears Admin when Granted is set.
func TestSessions_Set(t *testing.T) {
	s := NewSessions("my-signing-key", true)
	rec := httptest.NewRecorder()
	sess := Session{
		UserID:    "usr-1",
		Email:     "usr1@example.com",
		Name:      "User One",
		Admin:     true,
		AdminMode: true,
		SID:       "sid-123",
	}

	if err := s.Set(rec, sess); err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != sessionCookie {
		t.Fatalf("cookie name = %q, want %q", c.Name, sessionCookie)
	}
	if c.Path != "/" {
		t.Fatalf("cookie path = %q, want /", c.Path)
	}
	if !c.HttpOnly {
		t.Fatal("expected HttpOnly = true")
	}
	if !c.Secure {
		t.Fatal("expected Secure = true")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", c.SameSite)
	}
	expectedExp := time.Now().Add(12 * time.Hour)
	if diff := c.Expires.Sub(expectedExp); diff > 5*time.Second || diff < -5*time.Second {
		t.Fatalf("cookie expires %v unexpectedly far from %v", c.Expires, expectedExp)
	}
	if !strings.Contains(c.Value, ".") {
		t.Fatalf("cookie value missing dot separator: %q", c.Value)
	}

	// Granted clears Admin in the written cookie
	recGranted := httptest.NewRecorder()
	sessGranted := Session{
		UserID:    "usr-2",
		Admin:     true,
		AdminMode: true,
		Granted:   true,
	}
	if err := s.Set(recGranted, sessGranted); err != nil {
		t.Fatalf("Set(granted) error: %v", err)
	}
	reqGranted := httptest.NewRequest("GET", "/", nil)
	reqGranted.AddCookie(recGranted.Result().Cookies()[0])
	gotGranted, err := s.Get(reqGranted)
	if err != nil {
		t.Fatalf("Get(granted) error: %v", err)
	}
	if gotGranted.Admin {
		t.Fatal("Granted session must have Admin = false in saved cookie")
	}
	if gotGranted.Granted {
		t.Fatal("Granted field must not be persisted in cookie")
	}
}

// TestSessions_Get_RoundTrip: session round-trips correctly through Set and Get.
func TestSessions_Get_RoundTrip(t *testing.T) {
	s := NewSessions("roundtrip-secret", false)
	rec := httptest.NewRecorder()
	original := Session{
		UserID:    "user-99",
		Email:     "nine@nine.test",
		Name:      "Number Nine",
		Admin:     true,
		AdminMode: false,
		SID:       "device-session-uuid",
	}

	if err := s.Set(rec, original); err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	req := httptest.NewRequest("GET", "/test", nil)
	req.AddCookie(rec.Result().Cookies()[0])

	got, err := s.Get(req)
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if got.UserID != original.UserID {
		t.Errorf("UserID = %q, want %q", got.UserID, original.UserID)
	}
	if got.Email != original.Email {
		t.Errorf("Email = %q, want %q", got.Email, original.Email)
	}
	if got.Name != original.Name {
		t.Errorf("Name = %q, want %q", got.Name, original.Name)
	}
	if got.Admin != original.Admin {
		t.Errorf("Admin = %v, want %v", got.Admin, original.Admin)
	}
	if got.AdminMode != original.AdminMode {
		t.Errorf("AdminMode = %v, want %v", got.AdminMode, original.AdminMode)
	}
	if got.SID != original.SID {
		t.Errorf("SID = %q, want %q", got.SID, original.SID)
	}
	if got.Expires <= time.Now().Unix() {
		t.Errorf("Expires = %d, expected future timestamp", got.Expires)
	}
}

// TestSessions_Get_Errors: tests missing cookies, malformed values, signature mismatches, invalid JSON, and expiry.
func TestSessions_Get_Errors(t *testing.T) {
	s := NewSessions("error-testing-secret", false)

	// Valid session helper
	validCookieVal := func(sess Session) string {
		payload, err := json.Marshal(sess)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(payload) + "." + s.sign(payload)
	}

	normalSess := Session{UserID: "u1", Expires: time.Now().Add(time.Hour).Unix()}
	validVal := validCookieVal(normalSess)
	parts := strings.Split(validVal, ".")
	dataPart, sigPart := parts[0], parts[1]

	// Bad signature with another secret
	sOther := NewSessions("different-secret", false)
	otherVal := func() string {
		payload, _ := json.Marshal(normalSess)
		return base64.RawURLEncoding.EncodeToString(payload) + "." + sOther.sign(payload)
	}()

	// Non-JSON valid payload
	nonJSONPayload := []byte("this is not json")
	nonJSONVal := base64.RawURLEncoding.EncodeToString(nonJSONPayload) + "." + s.sign(nonJSONPayload)

	// Expired session
	expiredSess := Session{UserID: "u1", Expires: time.Now().Add(-10 * time.Minute).Unix()}
	expiredVal := validCookieVal(expiredSess)

	// Tampered data
	tamperedData := dataPart
	if tamperedData[0] == 'A' {
		tamperedData = "B" + tamperedData[1:]
	} else {
		tamperedData = "A" + tamperedData[1:]
	}

	// Tampered sig
	tamperedSig := sigPart
	if tamperedSig[0] == 'x' {
		tamperedSig = "y" + tamperedSig[1:]
	} else {
		tamperedSig = "x" + tamperedSig[1:]
	}

	tests := []struct {
		name      string
		cookie    *http.Cookie
		wantErr   string
		errTarget error
	}{
		{
			name:      "missing cookie",
			cookie:    nil,
			errTarget: http.ErrNoCookie,
		},
		{
			name:      "wrong cookie name",
			cookie:    &http.Cookie{Name: "other_cookie", Value: validVal},
			errTarget: http.ErrNoCookie,
		},
		{
			name:    "empty value",
			cookie:  &http.Cookie{Name: sessionCookie, Value: ""},
			wantErr: "malformed session",
		},
		{
			name:    "no dot delimiter",
			cookie:  &http.Cookie{Name: sessionCookie, Value: "juststrings"},
			wantErr: "malformed session",
		},
		{
			name:    "invalid base64 payload",
			cookie:  &http.Cookie{Name: sessionCookie, Value: "??invalid??." + sigPart},
			wantErr: "illegal base64 data",
		},
		{
			name:    "tampered payload",
			cookie:  &http.Cookie{Name: sessionCookie, Value: tamperedData + "." + sigPart},
			wantErr: "bad session signature",
		},
		{
			name:    "tampered signature",
			cookie:  &http.Cookie{Name: sessionCookie, Value: dataPart + "." + tamperedSig},
			wantErr: "bad session signature",
		},
		{
			name:    "signature signed with different secret",
			cookie:  &http.Cookie{Name: sessionCookie, Value: otherVal},
			wantErr: "bad session signature",
		},
		{
			name:    "empty signature",
			cookie:  &http.Cookie{Name: sessionCookie, Value: dataPart + "."},
			wantErr: "bad session signature",
		},
		{
			name:    "truncated signature",
			cookie:  &http.Cookie{Name: sessionCookie, Value: dataPart + "." + sigPart[:10]},
			wantErr: "bad session signature",
		},
		{
			name:    "valid signature but payload is not json",
			cookie:  &http.Cookie{Name: sessionCookie, Value: nonJSONVal},
			wantErr: "invalid character",
		},
		{
			name:    "expired session",
			cookie:  &http.Cookie{Name: sessionCookie, Value: expiredVal},
			wantErr: "session expired",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			_, err := s.Get(req)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.errTarget != nil && !errors.Is(err, tt.errTarget) {
				t.Fatalf("got err %v, want target %v", err, tt.errTarget)
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got err %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestSessions_Clear: clears the session cookie with MaxAge: -1 and empty value.
func TestSessions_Clear(t *testing.T) {
	for _, secure := range []bool{true, false} {
		s := NewSessions("clear-secret", secure)
		rec := httptest.NewRecorder()
		s.Clear(rec)

		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("expected 1 cookie, got %d", len(cookies))
		}
		c := cookies[0]
		if c.Name != sessionCookie {
			t.Errorf("cookie name = %q, want %q", c.Name, sessionCookie)
		}
		if c.Value != "" {
			t.Errorf("cookie value = %q, want empty", c.Value)
		}
		if c.MaxAge != -1 {
			t.Errorf("cookie maxage = %d, want -1", c.MaxAge)
		}
		if c.Path != "/" {
			t.Errorf("cookie path = %q, want /", c.Path)
		}
		if !c.HttpOnly {
			t.Error("expected HttpOnly = true")
		}
		if c.Secure != secure {
			t.Errorf("cookie secure = %v, want %v", c.Secure, secure)
		}
	}
}

// TestSessions_sign: internal sign helper generates reproducible URL-safe HMAC-SHA256 signatures.
func TestSessions_sign(t *testing.T) {
	s1 := NewSessions("key-1", false)
	s2 := NewSessions("key-2", false)
	payload := []byte("hello-go24")

	sig1 := s1.sign(payload)
	sig1Again := s1.sign(payload)
	if sig1 != sig1Again {
		t.Fatalf("sign is not deterministic: %q != %q", sig1, sig1Again)
	}

	sig2 := s2.sign(payload)
	if sig1 == sig2 {
		t.Fatal("different secrets must produce different signatures")
	}

	diffPayloadSig := s1.sign([]byte("different-payload"))
	if sig1 == diffPayloadSig {
		t.Fatal("different payloads must produce different signatures")
	}

	// Verify it decodes cleanly from RawURLEncoding without errors
	decoded, err := base64.RawURLEncoding.DecodeString(sig1)
	if err != nil {
		t.Fatalf("signature base64 decode failed: %v", err)
	}
	if len(decoded) != 32 { // sha256 output is 32 bytes
		t.Fatalf("expected 32 bytes sha256 output, got %d", len(decoded))
	}
}

// TestSessions_StampAndStamped: link stamps are 32 chars, sensitive to parts and order, and verifiable with Stamped.
func TestSessions_StampAndStamped(t *testing.T) {
	s := NewSessions("stamp-secret", false)
	sOther := NewSessions("other-secret", false)

	stamp := s.Stamp("invite", "user-42", "token-xyz")
	if len(stamp) != 32 {
		t.Fatalf("expected 32-char stamp, got len=%d (%q)", len(stamp), stamp)
	}

	// Matching parts
	if !s.Stamped(stamp, "invite", "user-42", "token-xyz") {
		t.Fatal("Stamped returned false for valid stamp and parts")
	}

	// No parts and single part
	emptyStamp := s.Stamp()
	if len(emptyStamp) != 32 {
		t.Fatalf("expected 32-char empty stamp, got len=%d", len(emptyStamp))
	}
	if !s.Stamped(emptyStamp) {
		t.Fatal("Stamped returned false for empty parts")
	}

	singleStamp := s.Stamp("one")
	if !s.Stamped(singleStamp, "one") {
		t.Fatal("Stamped returned false for single part")
	}

	// Negative cases
	tests := []struct {
		name  string
		stamp string
		parts []string
	}{
		{
			name:  "different secret",
			stamp: sOther.Stamp("invite", "user-42", "token-xyz"),
			parts: []string{"invite", "user-42", "token-xyz"},
		},
		{
			name:  "reordered parts",
			stamp: stamp,
			parts: []string{"invite", "token-xyz", "user-42"},
		},
		{
			name:  "extra part",
			stamp: stamp,
			parts: []string{"invite", "user-42", "token-xyz", "extra"},
		},
		{
			name:  "missing part",
			stamp: stamp,
			parts: []string{"invite", "user-42"},
		},
		{
			name:  "altered part",
			stamp: stamp,
			parts: []string{"invite", "user-43", "token-xyz"},
		},
		{
			name:  "tampered stamp char",
			stamp: stamp[:31] + "X",
			parts: []string{"invite", "user-42", "token-xyz"},
		},
		{
			name:  "truncated stamp",
			stamp: stamp[:16],
			parts: []string{"invite", "user-42", "token-xyz"},
		},
		{
			name:  "empty stamp",
			stamp: "",
			parts: []string{"invite", "user-42", "token-xyz"},
		},
		{
			name:  "extended stamp",
			stamp: stamp + "extra",
			parts: []string{"invite", "user-42", "token-xyz"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if s.Stamped(tt.stamp, tt.parts...) {
				t.Fatalf("Stamped unexpectedly returned true for %s", tt.name)
			}
		})
	}
}
