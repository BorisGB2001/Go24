package drive

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"go24/internal/i18n"
)

func TestDrivePath(t *testing.T) {
	for _, c := range []struct {
		name string
		d    Drive
		want string
	}{
		{
			name: "my drive (default)",
			d:    Drive{Kind: "user", OwnerID: "u1"},
			want: "/disk/my",
		},
		{
			name: "empty drive struct defaults to my",
			d:    Drive{},
			want: "/disk/my",
		},
		{
			name: "company drive",
			d:    Drive{Kind: "company"},
			want: "/disk/company",
		},
		{
			name: "project drive with key",
			d:    Drive{Kind: "project", ProjectKey: "HR"},
			want: "/disk/p/HR",
		},
		{
			name: "employee drive viewed by admin",
			d:    Drive{Kind: "user", OwnerID: "u-emp-1", Employee: true},
			want: "/disk/u/u-emp-1",
		},
		{
			name: "shared file drive",
			d:    Drive{File: "sf-file-99"},
			want: "/disk/sf/sf-file-99",
		},
		{
			name: "shared folder drive",
			d:    Drive{Root: "sh-folder-88"},
			want: "/disk/sh/sh-folder-88",
		},
		{
			name: "precedence: shared root overrides shared file",
			d:    Drive{Root: "sh-1", File: "sf-1"},
			want: "/disk/sh/sh-1",
		},
		{
			name: "precedence: shared file overrides employee",
			d:    Drive{File: "sf-2", Employee: true, OwnerID: "u2"},
			want: "/disk/sf/sf-2",
		},
		{
			name: "precedence: employee overrides company",
			d:    Drive{Kind: "company", Employee: true, OwnerID: "u3"},
			want: "/disk/u/u3",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.Path(); got != c.want {
				t.Errorf("Drive.Path() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDriveAt(t *testing.T) {
	dTop := Drive{Root: ""}
	if got := dTop.At(""); got != "" {
		t.Errorf("dTop.At(\"\") = %q, want empty", got)
	}
	if got := dTop.At("sub"); got != "sub" {
		t.Errorf("dTop.At(\"sub\") = %q, want sub", got)
	}

	dShared := Drive{Root: "root-folder"}
	if got := dShared.At(""); got != "root-folder" {
		t.Errorf("dShared.At(\"\") = %q, want root-folder", got)
	}
	if got := dShared.At("sub-folder"); got != "sub-folder" {
		t.Errorf("dShared.At(\"sub-folder\") = %q, want sub-folder", got)
	}
}

func TestDriveNotRoot(t *testing.T) {
	for _, c := range []struct {
		name    string
		d       Drive
		id      string
		wantErr bool
	}{
		{"normal drive allows any id", Drive{}, "f1", false},
		{"shared folder prohibits modifying root folder itself", Drive{Root: "f1"}, "f1", true},
		{"shared folder allows modifying child inside it", Drive{Root: "f1"}, "child", false},
		{"shared file prohibits modifying file", Drive{File: "file1"}, "file1", true},
		{"shared file prohibits modifying anything", Drive{File: "file1"}, "other", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.d.notRoot(c.id)
			if (err != nil) != c.wantErr {
				t.Errorf("Drive.notRoot(%q) error = %v, wantErr %v", c.id, err, c.wantErr)
			}
			if c.wantErr && !errors.Is(err, ErrForbidden) {
				t.Errorf("Drive.notRoot(%q) error = %v, want ErrForbidden", c.id, err)
			}
		})
	}
}

func TestDriveOwner(t *testing.T) {
	dNone := Drive{OwnerID: ""}
	if ptr := dNone.owner(); ptr != nil {
		t.Errorf("dNone.owner() = %v, want nil", ptr)
	}

	dSome := Drive{OwnerID: "u-100"}
	ptr := dSome.owner()
	if ptr == nil || *ptr != "u-100" {
		t.Errorf("dSome.owner() = %v, want u-100", ptr)
	}
}

func TestDriveMayChange(t *testing.T) {
	for _, c := range []struct {
		name   string
		d      Drive
		userID string
		by     string
		want   bool
	}{
		{"manager can change anything", Drive{CanManage: true}, "u1", "u2", true},
		{"author can change their own upload when write allowed", Drive{CanWrite: true}, "u1", "u1", true},
		{"author cannot change when write disallowed", Drive{CanWrite: false}, "u1", "u1", false},
		{"user cannot change another person's file without manage", Drive{CanWrite: true}, "u1", "u2", false},
		{"shared file with edit access can be changed by editor", Drive{File: "sf1", Access: "edit"}, "u1", "u2", true},
		{"shared file with read access cannot be changed by editor", Drive{File: "sf1", Access: "read"}, "u1", "u2", false},
		{"normal drive with edit access string but no file flag", Drive{Access: "edit"}, "u1", "u2", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.mayChange(c.userID, c.by); got != c.want {
				t.Errorf("Drive.mayChange(%q, %q) = %v, want %v", c.userID, c.by, got, c.want)
			}
		})
	}
}

func TestCheckName(t *testing.T) {
	errKey := "err.file_name"
	wantErr := i18n.E(errKey)

	for _, c := range []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"valid ascii file name", "report.pdf", "report.pdf", false},
		{"valid cyrillic file name", "договор.docx", "договор.docx", false},
		{"trims whitespace around name", "  budget.xlsx  ", "budget.xlsx", false},
		{"empty name rejected", "", "", true},
		{"spaces only rejected", "   \t\n  ", "", true},
		{"forward slash prohibited", "sub/file.txt", "", true},
		{"backward slash prohibited", "sub\\file.txt", "", true},
		{"exact 200 runes allowed", strings.Repeat("a", 200), strings.Repeat("a", 200), false},
		{"201 runes rejected", strings.Repeat("a", 201), "", true},
		{"multibyte runes counted by rune count", strings.Repeat("й", 200), strings.Repeat("й", 200), false},
		{"multibyte 201 runes rejected", strings.Repeat("й", 201), "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := checkName(c.input, errKey)
			if (err != nil) != c.wantErr {
				t.Fatalf("checkName(%q) err = %v, wantErr %v", c.input, err, c.wantErr)
			}
			if c.wantErr {
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != wantErr.Key {
					t.Errorf("checkName(%q) error = %v, want %v", c.input, err, wantErr)
				}
			} else if got != c.want {
				t.Errorf("checkName(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestFreeNameEdgeCases(t *testing.T) {
	for _, c := range []struct {
		name  string
		input string
		taken []string
		file  bool
		want  string
	}{
		{
			name:  "untaken file name kept unchanged",
			input: "notes.txt",
			taken: []string{"other.txt"},
			file:  true,
			want:  "notes.txt",
		},
		{
			name:  "taken file gets suffix (2) before extension",
			input: "notes.txt",
			taken: []string{"notes.txt"},
			file:  true,
			want:  "notes (2).txt",
		},
		{
			name:  "taken file with (2) increments to (3)",
			input: "notes.txt",
			taken: []string{"notes.txt", "notes (2).txt"},
			file:  true,
			want:  "notes (3).txt",
		},
		{
			name:  "taken folder gets suffix (2) at the end",
			input: "documents",
			taken: []string{"documents"},
			file:  false,
			want:  "documents (2)",
		},
		{
			name:  "folder with dots treats dots as part of name",
			input: "project.v1",
			taken: []string{"project.v1"},
			file:  false,
			want:  "project.v1 (2)",
		},
		{
			name:  "file with no extension appends number at the end",
			input: "README",
			taken: []string{"readme"},
			file:  true,
			want:  "README (2)",
		},
		{
			name:  "dotfile without separate extension",
			input: ".gitignore",
			taken: []string{".gitignore"},
			file:  true,
			want:  ".gitignore (2)",
		},
		{
			name:  "file with multiple dots preserves last extension",
			input: "archive.tar.gz",
			taken: []string{"archive.tar.gz"},
			file:  true,
			want:  "archive.tar (2).gz",
		},
		{
			name:  "case insensitive collision check",
			input: "REPORT.PDF",
			taken: []string{"report.pdf"},
			file:  true,
			want:  "REPORT (2).PDF",
		},
		{
			name:  "finds first available gap in numbering",
			input: "data.csv",
			taken: []string{"data.csv", "data (3).csv"},
			file:  true,
			want:  "data (2).csv",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := freeName(c.input, c.taken, c.file); got != c.want {
				t.Errorf("freeName(%q, file=%v) = %q, want %q", c.input, c.file, got, c.want)
			}
		})
	}
}

func TestNullable(t *testing.T) {
	if got := nullable(""); got != nil {
		t.Errorf("nullable(\"\") = %v, want nil", got)
	}
	s := "test"
	got := nullable(s)
	if got == nil || *got != s {
		t.Errorf("nullable(%q) = %v, want pointer to %q", s, got, s)
	}
}

func TestRandomHex(t *testing.T) {
	for _, n := range []int{0, 4, 8, 16, 32} {
		hexStr := randomHex(n)
		if len(hexStr) != n*2 {
			t.Errorf("randomHex(%d) length = %d, want %d", n, len(hexStr), n*2)
		}
		for _, r := range hexStr {
			if !(('0' <= r && r <= '9') || ('a' <= r && r <= 'f')) {
				t.Errorf("randomHex(%d) contains non-hex char: %c", n, r)
			}
		}
	}
	// Uniqueness check
	if s1, s2 := randomHex(16), randomHex(16); s1 == s2 {
		t.Errorf("consecutive randomHex(16) produced identical values: %q", s1)
	}
}

func TestNewStore(t *testing.T) {
	s := NewStore(nil, nil)
	if s == nil {
		t.Fatal("NewStore returned nil")
	}
	if s.db != nil || s.tasks != nil {
		t.Errorf("NewStore fields not properly set: db=%v tasks=%v", s.db, s.tasks)
	}
}

func TestStoreEarlyGuards(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	// CheckFolder: disallow write on read-only drive.
	if err := s.CheckFolder(ctx, Drive{CanWrite: false}, "f1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("CheckFolder(CanWrite=false) = %v, want ErrForbidden", err)
	}
	// CheckFolder: top of writable drive succeeds immediately.
	if err := s.CheckFolder(ctx, Drive{CanWrite: true, Root: ""}, ""); err != nil {
		t.Errorf("CheckFolder(writable top) = %v, want nil", err)
	}

	// EnsurePath: forbidden inside shared file drive.
	if _, err := s.EnsurePath(ctx, "u1", Drive{File: "sf1"}, "", "dir"); !errors.Is(err, ErrForbidden) {
		t.Errorf("EnsurePath(shared file) = %v, want ErrForbidden", err)
	}

	// DeleteFolder / DeleteFile: forbidden when attempting to delete shared root or within shared file.
	if err := s.DeleteFolder(ctx, "u1", Drive{Root: "r1"}, "r1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("DeleteFolder(root) = %v, want ErrForbidden", err)
	}
	if err := s.DeleteFolder(ctx, "u1", Drive{File: "f1"}, "any"); !errors.Is(err, ErrForbidden) {
		t.Errorf("DeleteFolder(shared file) = %v, want ErrForbidden", err)
	}
	if err := s.DeleteFile(ctx, "u1", Drive{File: "f1"}, "f1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("DeleteFile(shared file) = %v, want ErrForbidden", err)
	}

	// RenameFile: guards on root and name checks.
	if err := s.RenameFile(ctx, "u1", Drive{File: "f1"}, "f1", "name"); !errors.Is(err, ErrForbidden) {
		t.Errorf("RenameFile(shared file) = %v, want ErrForbidden", err)
	}
	if err := s.RenameFile(ctx, "u1", Drive{}, "f1", "invalid/name"); err == nil {
		t.Error("RenameFile(invalid/name) expected error, got nil")
	}

	// RenameFolder: guards on name and root checks.
	if err := s.RenameFolder(ctx, "u1", Drive{Root: "r1"}, "r1", "name"); !errors.Is(err, ErrForbidden) {
		t.Errorf("RenameFolder(root) = %v, want ErrForbidden", err)
	}
	if err := s.RenameFolder(ctx, "u1", Drive{}, "f1", "bad\\slash"); err == nil {
		t.Error("RenameFolder(bad\\slash) expected error, got nil")
	}

	// Move: forbidden from shared file drive.
	if err := s.Move(ctx, "u1", Drive{File: "f1"}, Selection{}, Drive{}, ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("Move(from shared file) = %v, want ErrForbidden", err)
	}

	// folderErr helper transforms unique key violation into localized folder exists error.
	pgUnique := &pgconn.PgError{Code: "23505"}
	gotErr := folderErr(pgUnique)
	var ie *i18n.Error
	if !errors.As(gotErr, &ie) || ie.Key != "err.folder_exists" {
		t.Errorf("folderErr(23505) = %v, want err.folder_exists", gotErr)
	}
	otherErr := errors.New("database failure")
	if got := folderErr(otherErr); !errors.Is(got, otherErr) {
		t.Errorf("folderErr(other) = %v, want otherErr", got)
	}
}
