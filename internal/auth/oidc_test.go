package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewOIDC: NewOIDC populates configuration fields and leaves provider uninitialized.
func TestNewOIDC(t *testing.T) {
	o := NewOIDC("https://auth.example.com", "client-id-1", "secret-key", "https://app.example.com/callback")
	if o.issuer != "https://auth.example.com" {
		t.Errorf("issuer = %q", o.issuer)
	}
	if o.clientID != "client-id-1" {
		t.Errorf("clientID = %q", o.clientID)
	}
	if o.clientSecret != "secret-key" {
		t.Errorf("clientSecret = %q", o.clientSecret)
	}
	if o.redirectURL != "https://app.example.com/callback" {
		t.Errorf("redirectURL = %q", o.redirectURL)
	}
	if o.oauth != nil {
		t.Error("expected oauth to be nil before init")
	}
	if o.verifier != nil {
		t.Error("expected verifier to be nil before init")
	}
}

// TestOIDC_AccountURL: AccountURL derives Authentik user settings page from issuer host or returns empty string.
func TestOIDC_AccountURL(t *testing.T) {
	tests := []struct {
		name   string
		issuer string
		want   string
	}{
		{
			name:   "https standard host",
			issuer: "https://auth.example.com",
			want:   "https://auth.example.com/if/user/#/settings",
		},
		{
			name:   "http with port and application path",
			issuer: "http://auth.local:9000/application/o/go24/",
			want:   "http://auth.local:9000/if/user/#/settings",
		},
		{
			name:   "trailing slash only",
			issuer: "https://idp.company.org/",
			want:   "https://idp.company.org/if/user/#/settings",
		},
		{
			name:   "empty string",
			issuer: "",
			want:   "",
		},
		{
			name:   "no host relative path",
			issuer: "relative/path",
			want:   "",
		},
		{
			name:   "scheme only",
			issuer: "http://",
			want:   "",
		},
		{
			name:   "invalid URL control chars",
			issuer: "://bad-url",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewOIDC(tt.issuer, "cid", "sec", "")
			if got := o.AccountURL(); got != tt.want {
				t.Fatalf("AccountURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClaims_JSON: Claims struct maps sub, email, name, preferred_username, groups, and amr.
func TestClaims_JSON(t *testing.T) {
	raw := `{
		"sub": "user-uuid-1",
		"email": "user@go24.test",
		"name": "Alex Smith",
		"preferred_username": "asmith",
		"groups": ["go24-admins", "employees"],
		"amr": ["pwd", "mfa"]
	}`

	var c Claims
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if c.Sub != "user-uuid-1" {
		t.Errorf("Sub = %q", c.Sub)
	}
	if c.Email != "user@go24.test" {
		t.Errorf("Email = %q", c.Email)
	}
	if c.Name != "Alex Smith" {
		t.Errorf("Name = %q", c.Name)
	}
	if c.Username != "asmith" {
		t.Errorf("Username = %q", c.Username)
	}
	if len(c.Groups) != 2 || c.Groups[0] != "go24-admins" || c.Groups[1] != "employees" {
		t.Errorf("Groups = %v", c.Groups)
	}
	if len(c.AMR) != 2 || c.AMR[0] != "pwd" || c.AMR[1] != "mfa" {
		t.Errorf("AMR = %v", c.AMR)
	}
}

// TestOIDC_Init_Error: init returns discovery error when issuer is unreachable or context is canceled.
func TestOIDC_Init_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before call

	o := NewOIDC("http://127.0.0.1:0", "client", "secret", "http://app.test/cb")
	if err := o.init(ctx); err == nil {
		t.Fatal("expected discovery error on canceled context, got nil")
	}

	// AuthCodeURL and Exchange bubble up init error
	if _, err := o.AuthCodeURL(ctx, "st", false); err == nil {
		t.Fatal("AuthCodeURL expected error when init fails")
	}
	if _, err := o.Exchange(ctx, "code"); err == nil {
		t.Fatal("Exchange expected error when init fails")
	}
}

// TestOIDC_AuthCodeURL_And_Exchange: tests lazy discovery, concurrent init, auth code URL generation,
// token exchange, signature verification, and username fallback.
func TestOIDC_AuthCodeURL_And_Exchange(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey error: %v", err)
	}

	const keyID = "test-key-id"
	eBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(eBytes, uint64(privKey.PublicKey.E))
	trimmedE := bytes.TrimLeft(eBytes, "\x00")
	jwksJSON, err := json.Marshal(map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": keyID,
				"n":   base64.RawURLEncoding.EncodeToString(privKey.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(trimmedE),
			},
		},
	})
	if err != nil {
		t.Fatalf("JWKS marshal error: %v", err)
	}

	makeIDToken := func(issuer, aud string, claims map[string]any) string {
		header := map[string]string{
			"alg": "RS256",
			"kid": keyID,
			"typ": "JWT",
		}
		hBytes, _ := json.Marshal(header)
		allClaims := map[string]any{
			"iss": issuer,
			"aud": aud,
			"exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(),
			"sub": "sub-12345",
		}
		for k, v := range claims {
			allClaims[k] = v
		}
		cBytes, _ := json.Marshal(allClaims)
		sigInput := base64.RawURLEncoding.EncodeToString(hBytes) + "." + base64.RawURLEncoding.EncodeToString(cBytes)
		h := sha256.Sum256([]byte(sigInput))
		sig, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, h[:])
		if err != nil {
			t.Fatalf("SignPKCS1v15 error: %v", err)
		}
		return sigInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	}

	var (
		serverURL     string
		tokenResponse map[string]any
		tokenStatus   int = http.StatusOK
		mu            sync.Mutex
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 serverURL,
				"authorization_endpoint": serverURL + "/oauth/authorize",
				"token_endpoint":         serverURL + "/oauth/token",
				"jwks_uri":               serverURL + "/oauth/jwks",
				"response_types_supported": []string{
					"code",
				},
			})
		case "/oauth/jwks":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(jwksJSON)
		case "/oauth/token":
			mu.Lock()
			status := tokenStatus
			resp := tokenResponse
			mu.Unlock()

			if status != http.StatusOK {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	serverURL = srv.URL

	ctx := context.Background()
	clientID := "go24-test-client"
	clientSecret := "go24-secret"
	redirectURL := "http://localhost:8080/auth/callback"

	o := NewOIDC(srv.URL, clientID, clientSecret, redirectURL)

	// Test concurrent init calls
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := o.init(ctx); err != nil {
				t.Errorf("concurrent init error: %v", err)
			}
		}()
	}
	wg.Wait()

	// Subsequent init returns nil immediately
	if err := o.init(ctx); err != nil {
		t.Fatalf("repeated init error: %v", err)
	}

	// AuthCodeURL without again
	url1, err := o.AuthCodeURL(ctx, "state-one", false)
	if err != nil {
		t.Fatalf("AuthCodeURL error: %v", err)
	}
	u1, err := url.Parse(url1)
	if err != nil {
		t.Fatalf("Parse(url1) error: %v", err)
	}
	q1 := u1.Query()
	if q1.Get("client_id") != clientID {
		t.Errorf("client_id = %q", q1.Get("client_id"))
	}
	if q1.Get("redirect_uri") != redirectURL {
		t.Errorf("redirect_uri = %q", q1.Get("redirect_uri"))
	}
	if q1.Get("state") != "state-one" {
		t.Errorf("state = %q", q1.Get("state"))
	}
	if q1.Get("prompt") != "" {
		t.Errorf("expected no prompt param, got %q", q1.Get("prompt"))
	}

	// AuthCodeURL with again=true forces prompt=login
	url2, err := o.AuthCodeURL(ctx, "state-two", true)
	if err != nil {
		t.Fatalf("AuthCodeURL(again=true) error: %v", err)
	}
	u2, err := url.Parse(url2)
	if err != nil {
		t.Fatalf("Parse(url2) error: %v", err)
	}
	if u2.Query().Get("prompt") != "login" {
		t.Errorf("expected prompt=login, got %q", u2.Query().Get("prompt"))
	}

	// Exchange failure: token endpoint error
	mu.Lock()
	tokenStatus = http.StatusBadRequest
	tokenResponse = nil
	mu.Unlock()

	if _, err := o.Exchange(ctx, "bad-code"); err == nil || !strings.Contains(err.Error(), "token exchange") {
		t.Fatalf("expected token exchange error, got: %v", err)
	}

	// Exchange failure: no id_token in token response
	mu.Lock()
	tokenStatus = http.StatusOK
	tokenResponse = map[string]any{
		"access_token": "acc-123",
		"token_type":   "Bearer",
	}
	mu.Unlock()

	if _, err := o.Exchange(ctx, "code-no-idtoken"); err == nil || !strings.Contains(err.Error(), "no id_token") {
		t.Fatalf("expected no id_token error, got: %v", err)
	}

	// Exchange failure: invalid id_token signature or format
	mu.Lock()
	tokenResponse = map[string]any{
		"access_token": "acc-123",
		"token_type":   "Bearer",
		"id_token":     "not-a-valid-jwt",
	}
	mu.Unlock()

	if _, err := o.Exchange(ctx, "code-invalid-token"); err == nil || !strings.Contains(err.Error(), "verify id_token") {
		t.Fatalf("expected verify id_token error, got: %v", err)
	}

	// Exchange failure: unmarshal error on claims (e.g. groups is int instead of array)
	mu.Lock()
	tokenResponse = map[string]any{
		"access_token": "acc-123",
		"token_type":   "Bearer",
		"id_token": makeIDToken(srv.URL, clientID, map[string]any{
			"groups": 12345, // invalid type for []string
		}),
	}
	mu.Unlock()

	if _, err := o.Exchange(ctx, "code-bad-claims"); err == nil {
		t.Fatal("expected claims unmarshal error, got nil")
	}

	// Exchange success with Name set
	mu.Lock()
	tokenResponse = map[string]any{
		"access_token": "acc-123",
		"token_type":   "Bearer",
		"id_token": makeIDToken(srv.URL, clientID, map[string]any{
			"email":              "alice@test.org",
			"name":               "Alice Wonderland",
			"preferred_username": "alicew",
			"groups":             []string{"staff", "it"},
			"amr":                []string{"pwd"},
		}),
	}
	mu.Unlock()

	claims1, err := o.Exchange(ctx, "valid-code-with-name")
	if err != nil {
		t.Fatalf("Exchange error: %v", err)
	}
	if claims1.Sub != "sub-12345" {
		t.Errorf("Sub = %q, want sub-12345", claims1.Sub)
	}
	if claims1.Email != "alice@test.org" {
		t.Errorf("Email = %q, want alice@test.org", claims1.Email)
	}
	if claims1.Name != "Alice Wonderland" {
		t.Errorf("Name = %q, want Alice Wonderland", claims1.Name)
	}
	if claims1.Username != "alicew" {
		t.Errorf("Username = %q, want alicew", claims1.Username)
	}
	if len(claims1.Groups) != 2 || claims1.Groups[0] != "staff" {
		t.Errorf("Groups = %v", claims1.Groups)
	}

	// Exchange success with Name empty: fallback to Username
	mu.Lock()
	tokenResponse = map[string]any{
		"access_token": "acc-456",
		"token_type":   "Bearer",
		"id_token": makeIDToken(srv.URL, clientID, map[string]any{
			"email":              "bob@test.org",
			"name":               "",
			"preferred_username": "bob_builder",
		}),
	}
	mu.Unlock()

	claims2, err := o.Exchange(ctx, "valid-code-empty-name")
	if err != nil {
		t.Fatalf("Exchange(fallback name) error: %v", err)
	}
	if claims2.Name != "bob_builder" {
		t.Errorf("Name fallback = %q, want bob_builder", claims2.Name)
	}
	if claims2.Username != "bob_builder" {
		t.Errorf("Username = %q, want bob_builder", claims2.Username)
	}
}
