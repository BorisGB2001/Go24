package drive

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestLinkExpired(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	for _, c := range []struct {
		name      string
		expiresAt *time.Time
		want      bool
	}{
		{"nil expiration never expires", nil, false},
		{"future expiration has not expired", &future, false},
		{"exact current timestamp has not expired (must be after)", &now, false},
		{"past expiration has expired", &past, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := Link{ExpiresAt: c.expiresAt}
			if got := l.Expired(now); got != c.want {
				t.Errorf("Link.Expired() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSharedCheckPassword(t *testing.T) {
	// Links without a password set allow any access.
	shNoPass := Shared{Link: Link{Password: false}}
	if !shNoPass.CheckPassword("") || !shNoPass.CheckPassword("anything") {
		t.Error("Shared without password should accept any password")
	}

	// Links with a password check the provided bcrypt hash.
	rawPass := "secret123"
	hash, err := bcrypt.GenerateFromPassword([]byte(rawPass), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to generate bcrypt hash: %v", err)
	}

	shPass := Shared{Link: Link{Password: true}, hash: string(hash)}
	if !shPass.CheckPassword("secret123") {
		t.Error("CheckPassword(\"secret123\") with valid hash = false, want true")
	}
	if shPass.CheckPassword("wrong-password") {
		t.Error("CheckPassword(\"wrong-password\") = true, want false")
	}
	if shPass.CheckPassword("") {
		t.Error("CheckPassword(\"\") on password-protected link = true, want false")
	}
}

func TestSharedPasswordStamp(t *testing.T) {
	for _, c := range []struct {
		name string
		hash string
		want string
	}{
		{"empty hash returns empty stamp", "", ""},
		{"short hash under 20 chars returns empty", "too-short", ""},
		{"19 chars returns empty", strings.Repeat("x", 19), ""},
		{"exactly 20 chars returns entire string", "12345678901234567890", "12345678901234567890"},
		{"longer hash returns last 20 characters", "prefix-part-012345678901234567890", "12345678901234567890"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sh := Shared{hash: c.hash}
			if got := sh.PasswordStamp(); got != c.want {
				t.Errorf("Shared.PasswordStamp() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStoreLinksGuards(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	// Read-only drives cannot create, delete, or inspect public links.
	dReadOnly := Drive{CanWrite: false}
	if _, err := s.SetLink(ctx, "u1", dReadOnly, "file", "f1", "", false, nil, false, ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("SetLink(CanWrite=false) = %v, want ErrForbidden", err)
	}
	if err := s.DeleteLink(ctx, "u1", dReadOnly, "file", "f1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("DeleteLink(CanWrite=false) = %v, want ErrForbidden", err)
	}
	if _, err := s.LinkOf(ctx, dReadOnly, "file", "f1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("LinkOf(CanWrite=false) = %v, want ErrForbidden", err)
	}
}
