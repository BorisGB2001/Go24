package db

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var migrationPattern = regexp.MustCompile(`^(\d{3}|[1-9]\d{3,})_([a-z0-9_]+)\.sql$`)

// parseMigrationFilename validates the NNN_name.sql (from 1000 — NNNN_name.sql) naming convention and extracts the migration number and name.
func parseMigrationFilename(path string) (num int, name string, err error) {
	base := filepath.Base(path)
	m := migrationPattern.FindStringSubmatch(base)
	if m == nil {
		return 0, "", fmt.Errorf("invalid migration filename format: %q", base)
	}
	num, err = strconv.Atoi(m[1])
	if err != nil {
		return 0, "", fmt.Errorf("invalid migration number %q: %w", m[1], err)
	}
	return num, m[2], nil
}

// TestEmbeddedMigrations reads all embedded migrations and verifies:
// 1. Every migration filename matches NNN_name.sql format with a 3-digit prefix.
// 2. Migration numbers are unique (no two streams have collided on numbers).
// 3. Every migration file is non-empty.
// 4. Migrations sort in strictly increasing apply order (lexical sort matches numerical sort).
func TestEmbeddedMigrations(t *testing.T) {
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("Glob migrations/*.sql failed: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no migrations found in embedded filesystem")
	}

	seenNumbers := make(map[int]string, len(names))
	prevNum := -1
	prevPath := ""

	sortedNames := make([]string, len(names))
	copy(sortedNames, names)
	sortMigrations(sortedNames)

	for _, path := range sortedNames {
		num, name, err := parseMigrationFilename(path)
		if err != nil {
			t.Errorf("migration %q invalid format: %v", path, err)
			continue
		}
		if name == "" {
			t.Errorf("migration %q has empty description", path)
		}

		// Numbers must be unique across all migrations.
		if existing, dup := seenNumbers[num]; dup {
			t.Errorf("duplicate migration number %03d: %q and %q", num, existing, path)
		} else {
			seenNumbers[num] = path
		}

		// Files must be non-empty and not just whitespace.
		content, err := migrationsFS.ReadFile(path)
		if err != nil {
			t.Errorf("ReadFile(%q) error: %v", path, err)
		} else {
			if len(content) == 0 {
				t.Errorf("migration %q is empty", path)
			}
			if len(strings.TrimSpace(string(content))) == 0 {
				t.Errorf("migration %q contains only whitespace", path)
			}
		}

		// Migrations must sort in strictly increasing apply order.
		if num <= prevNum {
			t.Errorf("migration %q (number %d) does not sort in apply order after %q (number %d)", path, num, prevPath, prevNum)
		}
		prevNum = num
		prevPath = path
	}

	// Verify that the apply order is strictly the numeric order.
	byNum := make([]string, len(sortedNames))
	copy(byNum, sortedNames)
	sort.Slice(byNum, func(i, j int) bool {
		numI, _, _ := parseMigrationFilename(byNum[i])
		numJ, _, _ := parseMigrationFilename(byNum[j])
		return numI < numJ
	})
	for i := range sortedNames {
		if sortedNames[i] != byNum[i] {
			t.Errorf("sort order mismatch at index %d: applied=%q, numeric=%q", i, sortedNames[i], byNum[i])
		}
	}
}

// TestSortMigrations: four digits go after three.
func TestSortMigrations(t *testing.T) {
	names := []string{"migrations/1000_a.sql", "migrations/101_b.sql", "migrations/999_c.sql", "migrations/100_d.sql", "migrations/1001_e.sql"}
	sortMigrations(names)
	want := []string{"migrations/100_d.sql", "migrations/101_b.sql", "migrations/999_c.sql", "migrations/1000_a.sql", "migrations/1001_e.sql"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("sortMigrations = %v, want %v", names, want)
	}
}

// TestParseMigrationFilename verifies migration filename pattern matching against edge cases.
func TestParseMigrationFilename(t *testing.T) {
	tests := []struct {
		input    string
		wantNum  int
		wantName string
		wantErr  bool
	}{
		// Valid cases
		{input: "001_init.sql", wantNum: 1, wantName: "init"},
		{input: "010_stages.sql", wantNum: 10, wantName: "stages"},
		{input: "100_feed_marks.sql", wantNum: 100, wantName: "feed_marks"},
		{input: "223_crm_product_props.sql", wantNum: 223, wantName: "crm_product_props"},
		{input: "597_booking_moved.sql", wantNum: 597, wantName: "booking_moved"},
		{input: "migrations/001_init.sql", wantNum: 1, wantName: "init"},
		{input: "path/to/042_project_invites.sql", wantNum: 42, wantName: "project_invites"},
		{input: "1000_doc_template_fields.sql", wantNum: 1000, wantName: "doc_template_fields"},

		// Invalid cases - number prefix length
		{input: "", wantErr: true},
		{input: "init.sql", wantErr: true},
		{input: "1_init.sql", wantErr: true},
		{input: "01_init.sql", wantErr: true},
		{input: "0001_init.sql", wantErr: true},
		{input: "abc_init.sql", wantErr: true},
		{input: "-01_init.sql", wantErr: true},

		// Invalid cases - name and separator
		{input: "001.sql", wantErr: true},
		{input: "001_.sql", wantErr: true},
		{input: "001-init.sql", wantErr: true},
		{input: "001_Init.sql", wantErr: true},
		{input: "001_init_with space.sql", wantErr: true},

		// Invalid cases - extension
		{input: "001_init", wantErr: true},
		{input: "001_init.SQL", wantErr: true},
		{input: "001_init.txt", wantErr: true},
		{input: "001_init.sql.bak", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			num, name, err := parseMigrationFilename(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseMigrationFilename(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr {
				if num != tt.wantNum || name != tt.wantName {
					t.Errorf("parseMigrationFilename(%q) = (%d, %q), want (%d, %q)", tt.input, num, name, tt.wantNum, tt.wantName)
				}
			}
		})
	}
}

// TestMigrationsFSRead tests reading embedded files and directories through migrationsFS.
func TestMigrationsFSRead(t *testing.T) {
	// Reading an existing migration file
	content, err := migrationsFS.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("ReadFile(migrations/001_init.sql) error: %v", err)
	}
	if len(content) == 0 {
		t.Error("migrations/001_init.sql is empty")
	}

	// Reading a non-existent migration file
	if _, err := migrationsFS.ReadFile("migrations/999_nonexistent.sql"); err == nil {
		t.Error("expected error reading non-existent file, got nil")
	}

	// Opening the migrations directory
	dir, err := migrationsFS.Open("migrations")
	if err != nil {
		t.Fatalf("Open(migrations) error: %v", err)
	}
	defer dir.Close()

	stat, err := dir.Stat()
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	if !stat.IsDir() {
		t.Error("migrations is not a directory")
	}

	// Glob pattern matching
	matches, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("Glob error: %v", err)
	}
	if len(matches) == 0 {
		t.Error("expected at least one match, got 0")
	}

	noMatches, err := fs.Glob(migrationsFS, "migrations/*.nonexistent")
	if err != nil {
		t.Fatalf("Glob error: %v", err)
	}
	if len(noMatches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(noMatches))
	}
}

// TestMigrationsDirEntries checks that the migrations directory contains only SQL migration files.
func TestMigrationsDirEntries(t *testing.T) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("ReadDir(migrationsFS, migrations) error: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Errorf("unexpected subdirectory in migrations: %s", entry.Name())
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".sql") {
			t.Errorf("non-SQL file in migrations directory: %s", entry.Name())
		}
	}
}
