package drive

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAccessesConstants(t *testing.T) {
	if !slices.Equal(Accesses, []string{"read", "edit", "full"}) {
		t.Errorf("Accesses = %v, want [read edit full]", Accesses)
	}
	if !slices.Equal(FileAccesses, []string{"read", "edit"}) {
		t.Errorf("FileAccesses = %v, want [read edit]", FileAccesses)
	}
}

func TestLeftURL(t *testing.T) {
	if got := leftURL(true, "file-123"); got != "/files/file-123/about" {
		t.Errorf("leftURL(file=true) = %q, want /files/file-123/about", got)
	}
	if got := leftURL(false, "folder-456"); got != "/disk/my?folder=folder-456" {
		t.Errorf("leftURL(file=false) = %q, want /disk/my?folder=folder-456", got)
	}
}

func TestIsFKOrUUID(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"generic error", errors.New("something went wrong"), false},
		{"foreign key violation 23503", &pgconn.PgError{Code: "23503"}, true},
		{"invalid uuid syntax 22P02", &pgconn.PgError{Code: "22P02"}, true},
		{"unique violation 23505", &pgconn.PgError{Code: "23505"}, false},
		{"wrapped foreign key violation", fmt.Errorf("query failed: %w", &pgconn.PgError{Code: "23503"}), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := isFKOrUUID(c.err); got != c.want {
				t.Errorf("isFKOrUUID(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestSetShareValidation(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	// Only personal user drives can be shared.
	if err := s.SetShare(ctx, "u1", Drive{Kind: "company"}, "f1", "", "user", "u2", "read"); !errors.Is(err, ErrForbidden) {
		t.Errorf("SetShare(company drive) = %v, want ErrForbidden", err)
	}
	if err := s.SetShare(ctx, "u1", Drive{Kind: "user", Root: "sh1"}, "f1", "", "user", "u2", "read"); !errors.Is(err, ErrForbidden) {
		t.Errorf("SetShare(connected shared folder) = %v, want ErrForbidden", err)
	}
	if err := s.SetShare(ctx, "u1", Drive{Kind: "user", File: "sf1"}, "f1", "", "user", "u2", "read"); !errors.Is(err, ErrForbidden) {
		t.Errorf("SetShare(shared file) = %v, want ErrForbidden", err)
	}
	if err := s.SetShare(ctx, "u1", Drive{Kind: "user", OwnerID: "other"}, "f1", "", "user", "u2", "read"); !errors.Is(err, ErrForbidden) {
		t.Errorf("SetShare(not drive owner) = %v, want ErrForbidden", err)
	}

	ownerDrive := Drive{Kind: "user", OwnerID: "u1"}
	// Cannot share with yourself.
	if err := s.SetShare(ctx, "u1", ownerDrive, "f1", "", "user", "u1", "read"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetShare(with self) = %v, want ErrNotFound", err)
	}
	// Invalid kind.
	if err := s.SetShare(ctx, "u1", ownerDrive, "f1", "", "bogus_kind", "u2", "read"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetShare(invalid kind) = %v, want ErrNotFound", err)
	}
	// Empty whomID.
	if err := s.SetShare(ctx, "u1", ownerDrive, "f1", "", "user", "", "read"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetShare(empty whomID) = %v, want ErrNotFound", err)
	}
	// Invalid folder access.
	if err := s.SetShare(ctx, "u1", ownerDrive, "f1", "", "user", "u2", "superuser"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetShare(invalid access) = %v, want ErrNotFound", err)
	}
	// Single file share cannot have "full" access (only "read" and "edit").
	if err := s.SetShare(ctx, "u1", ownerDrive, "", "file1", "user", "u2", "full"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetShare(file with full access) = %v, want ErrNotFound", err)
	}
}

func TestShareFileWithValidation(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	// Only personal drive owner may share file with chat members.
	if err := s.ShareFileWith(ctx, "u1", Drive{Kind: "company"}, "f1", []string{"u2"}, "read"); !errors.Is(err, ErrForbidden) {
		t.Errorf("ShareFileWith(company) = %v, want ErrForbidden", err)
	}
	// Invalid file access.
	if err := s.ShareFileWith(ctx, "u1", Drive{Kind: "user", OwnerID: "u1"}, "f1", []string{"u2"}, "full"); !errors.Is(err, ErrForbidden) {
		t.Errorf("ShareFileWith(full access) = %v, want ErrForbidden", err)
	}
}

func TestSharesOfAndDeleteShareValidation(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	if _, err := s.SharesOf(ctx, "u1", Drive{Kind: "company"}, "f1", ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("SharesOf(company) = %v, want ErrForbidden", err)
	}
	if err := s.DeleteShare(ctx, "u1", Drive{Kind: "company"}, "share-1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("DeleteShare(company) = %v, want ErrForbidden", err)
	}
}

func TestSharesEmpty(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	got, err := s.Shares(ctx, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("Shares(nil) = (%v, %v), want empty map", got, err)
	}
	got, err = s.Shares(ctx, []string{})
	if err != nil || len(got) != 0 {
		t.Errorf("Shares([]) = (%v, %v), want empty map", got, err)
	}
}
