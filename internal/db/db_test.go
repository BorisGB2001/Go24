package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestOpenContextCanceled verifies that Open returns context.Canceled immediately if the context is already canceled.
func TestOpenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pool, err := Open(ctx, "postgres://user:pass@127.0.0.1:5432/go24?sslmode=disable")
	if pool != nil {
		t.Errorf("Open returned non-nil pool: %v", pool)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Open err = %v, want context.Canceled", err)
	}
}

// TestOpenContextDeadlineExceeded verifies that Open returns context.DeadlineExceeded when the context deadline has expired.
func TestOpenContextDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	pool, err := Open(ctx, "postgres://user:pass@127.0.0.1:5432/go24?sslmode=disable")
	if pool != nil {
		t.Errorf("Open returned non-nil pool: %v", pool)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Open err = %v, want context.DeadlineExceeded", err)
	}
}

// TestOpenContextTimeoutDuringRetry verifies that Open aborts retrying promptly when the context times out.
func TestOpenContextTimeoutDuringRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	pool, err := Open(ctx, "postgres://127.0.0.1:1/nonexistent?connect_timeout=1")
	elapsed := time.Since(start)

	if pool != nil {
		t.Errorf("Open returned non-nil pool: %v", pool)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("Open err = %v, want context.DeadlineExceeded or context.Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Open took %v, retry loop should have aborted promptly on context timeout", elapsed)
	}
}

// TestOpenInvalidURLs verifies error handling for various invalid URL shapes with a canceled context.
func TestOpenInvalidURLs(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "empty string", url: ""},
		{name: "malformed URL", url: "://bad-url"},
		{name: "invalid scheme", url: "ftp://user:pass@localhost:5432/db"},
		{name: "invalid characters", url: "postgres://user:pass@bad host:5432/db"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			pool, err := Open(ctx, tc.url)
			if pool != nil {
				t.Errorf("Open(%q) returned non-nil pool: %v", tc.url, pool)
			}
			if err == nil {
				t.Errorf("Open(%q) expected error, got nil", tc.url)
			}
		})
	}
}

// TestMigrateContextCanceled verifies that Migrate fails fast without connecting when context is canceled.
func TestMigrateContextCanceled(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:5432/go24?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.New failed: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = Migrate(ctx, pool)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Migrate err = %v, want context.Canceled", err)
	}
}

// TestMigrateContextDeadlineExceeded verifies that Migrate fails fast when context deadline has passed.
func TestMigrateContextDeadlineExceeded(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:5432/go24?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.New failed: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err = Migrate(ctx, pool)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Migrate err = %v, want context.DeadlineExceeded", err)
	}
}
