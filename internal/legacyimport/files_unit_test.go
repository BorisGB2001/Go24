package legacyimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go24/internal/anon"
)

func TestSafeName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "standard filename", in: "document.pdf", want: "document.pdf"},
		{name: "forward slashes", in: "path/to/nested/file.txt", want: "path_to_nested_file.txt"},
		{name: "backslashes", in: `path\to\nested\file.txt`, want: "path_to_nested_file.txt"},
		{name: "mixed slashes", in: `a/b\c/d.txt`, want: "a_b_c_d.txt"},
		{name: "surrounding whitespace", in: "   file.txt  \t\n", want: "file.txt"},
		{name: "empty string", in: "", want: "file"},
		{name: "whitespace only", in: "   \t\n  ", want: "file"},
		{name: "single dot", in: ".", want: "file"},
		{name: "double dot", in: "..", want: "file"},
		{name: "single dot with spaces", in: "  .  ", want: "file"},
		{name: "double dot with spaces", in: "  ..  ", want: "file"},
		{name: "triple dot", in: "...", want: "..."},
		{name: "hidden file dotfile", in: ".gitignore", want: ".gitignore"},
		{name: "leading and trailing slashes", in: "/dir/sub/", want: "_dir_sub_"},
		{name: "slashes only", in: "///", want: "___"},
		{name: "backslash only", in: `\`, want: "_"},
		{name: "slash with spaces", in: " / ", want: "_"},
		{name: "spaces inside filename", in: "quarterly report 2026.xlsx", want: "quarterly report 2026.xlsx"},
		{name: "unicode characters", in: "Отчёт_2026.docx", want: "Отчёт_2026.docx"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeName(tc.in)
			if got != tc.want {
				t.Errorf("safeName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestImportFiles_ValidationErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("missing directory", func(t *testing.T) {
		opts := Options{Dir: filepath.Join(t.TempDir(), "nonexistent")}
		n, err := ImportFiles(ctx, nil, nil, opts)
		if err == nil {
			t.Fatal("expected error for nonexistent directory, got nil")
		}
		if n != 0 {
			t.Fatalf("expected 0 added files, got %d", n)
		}
	})

	t.Run("invalid users.tsv column count", func(t *testing.T) {
		dir := t.TempDir()
		// users.tsv requires 7 columns; write 5
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte("1\t2\t3\t4\t5\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{Dir: dir}
		n, err := ImportFiles(ctx, nil, nil, opts)
		if err == nil || !strings.Contains(err.Error(), "columns, want 7") {
			t.Fatalf("expected 7 columns error, got: %v", err)
		}
		if n != 0 {
			t.Fatalf("expected 0 added files, got %d", n)
		}
	})

	t.Run("missing files.tsv", func(t *testing.T) {
		dir := t.TempDir()
		// users.tsv valid with 7 columns
		userRow := "1\t" + e("Name") + "\t" + e("Last") + "\t\tM\t" + e("mail@example.com") + "\t" + e("login") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte(userRow), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{
			Dir:  dir,
			Anon: anon.New("seed", "example.com"),
		}
		n, err := ImportFiles(ctx, nil, nil, opts)
		if err == nil {
			t.Fatal("expected error for missing files.tsv, got nil")
		}
		if n != 0 {
			t.Fatalf("expected 0 added files, got %d", n)
		}
	})

	t.Run("invalid files.tsv column count", func(t *testing.T) {
		dir := t.TempDir()
		userRow := "1\t" + e("Name") + "\t" + e("Last") + "\t\tM\t" + e("mail@example.com") + "\t" + e("login") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte(userRow), 0o600); err != nil {
			t.Fatal(err)
		}
		// files.tsv requires 7 columns; write 4
		if err := os.WriteFile(filepath.Join(dir, "files.tsv"), []byte("100\t200\tname\ttask\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{
			Dir:  dir,
			Anon: anon.New("seed", "example.com"),
		}
		n, err := ImportFiles(ctx, nil, nil, opts)
		if err == nil || !strings.Contains(err.Error(), "columns, want 7") {
			t.Fatalf("expected 7 columns error for files.tsv, got: %v", err)
		}
		if n != 0 {
			t.Fatalf("expected 0 added files, got %d", n)
		}
	})
}
