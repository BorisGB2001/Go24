package apikeys

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestParseToken(t *testing.T) {
	good := "go24_" + strings.Repeat("a", 12) + "_" + strings.Repeat("b", 64)
	if id, sec, ok := ParseToken(good); !ok || id != strings.Repeat("a", 12) || len(sec) != 64 {
		t.Fatalf("good token rejected: %v %q %q", ok, id, sec)
	}
	for _, bad := range []string{
		"", "go24_", "Bearer " + good, "go23_" + good[5:],
		"go24_" + strings.Repeat("a", 11) + "_" + strings.Repeat("b", 64),
		"go24_" + strings.Repeat("z", 12) + "_" + strings.Repeat("b", 64),
		"go24_" + strings.Repeat("a", 12) + "_" + strings.Repeat("b", 63),
	} {
		if _, _, ok := ParseToken(bad); ok {
			t.Errorf("bad token accepted: %q", bad)
		}
	}
}

// TestKeyLifecycle needs GO24_TEST_DATABASE_URL (throwaway DB, schema is recreated).
func TestKeyLifecycle(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var uid string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('k', 'k') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}

	s := NewStore(pool)
	if _, err := s.Create(ctx, uid, "x", []string{"bogus"}, 0); err == nil {
		t.Fatal("key without valid scopes must fail")
	}
	token, err := s.Create(ctx, uid, "CI", []string{"tasks:read", "tasks:read", "bogus"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, token, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != uid || !p.Can("tasks:read") || p.Can("tasks:write") || len(p.Scopes) != 1 {
		t.Fatalf("principal = %+v", p)
	}
	wrong := "0" // another last character (the same one would be the right token)
	if strings.HasSuffix(token, "0") {
		wrong = "1"
	}
	if _, err := s.Authenticate(ctx, token[:len(token)-1]+wrong, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong secret: %v", err)
	}

	keys, err := s.List(ctx, uid)
	if err != nil || len(keys) != 1 || keys[0].LastUsedIP != "10.0.0.1" || !keys[0].Active() {
		t.Fatalf("list = %+v, %v", keys, err)
	}
	if err := s.Revoke(ctx, uid, keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revoked key must fail, got %v", err)
	}
}
