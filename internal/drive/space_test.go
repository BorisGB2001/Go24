package drive

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestDriveSpace: «Очистка» splits the used place into files, former versions and the trash (a trashed folder's
// files with it), lists the biggest files; the former versions go (all or one file's), the trash is emptied; only the
// one who manages the drive cleans it; the versions kept are trimmed at a new version.
func TestDriveSpace(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'i@t') RETURNING id`).Scan(&ivan))
	s := NewStore(pool, tasks.NewStore(pool))
	my, _ := s.Open(ctx, anna, "my")
	co, _ := s.Open(ctx, ivan, "company") // not an administrator
	add := func(name, folder string, size int64, deleted bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id, deleted_at)
			VALUES (gen_random_uuid()::text, $1, $2, $3, 'user', $3, $4, CASE WHEN $5 THEN now() END) RETURNING id`,
			name, size, anna, nullable(folder), deleted).Scan(&id))
		return id
	}
	docs, err := s.CreateFolder(ctx, anna, my, "", "Документы")
	must(err)
	old, err := s.CreateFolder(ctx, anna, my, "", "Старое")
	must(err)
	big := add("большой.zip", docs, 500, false)
	plan := add("план.docx", "", 100, false)
	add("удалён.txt", "", 50, true)
	add("в папке.txt", old, 70, false)
	must(s.DeleteFolder(ctx, anna, my, old)) // its file goes with it
	must(s.AddVersion(ctx, anna, my, plan, NewVersion{Key: "p2", Size: 120, ContentType: "x"}))
	must(s.AddVersion(ctx, anna, my, plan, NewVersion{Key: "p3", Size: 130, ContentType: "x"}))
	must(s.AddVersion(ctx, anna, my, big, NewVersion{Key: "b2", Size: 400, ContentType: "x"}))

	sp, err := s.Space(ctx, my)
	must(err)
	if sp.Files != 530 || sp.FileCount != 2 || sp.Versions != 720 || sp.VersionCount != 3 || sp.Trash != 120 || sp.TrashCount != 2 {
		t.Fatalf("space = %+v", sp)
	}
	if u, _ := s.Usage(ctx, my); u.Used != sp.Used() {
		t.Errorf("usage %d ≠ space %d", u.Used, sp.Used())
	}
	if len(sp.Big) != 2 || sp.Big[0].ID != big || sp.Big[0].Total() != 900 || sp.Big[0].Where != "Документы" || sp.Big[0].FolderID != docs ||
		sp.Big[1].ID != plan || sp.Big[1].Versions != 2 || sp.Big[1].VersionsSize != 220 {
		t.Errorf("biggest = %+v", sp.Big)
	}
	// not the one who manages the drive: neither seen nor cleaned
	if _, err := s.Space(ctx, co); !errors.Is(err, ErrForbidden) {
		t.Errorf("space of the company's drive for ivan: %v", err)
	}
	if _, err := s.PurgeVersions(ctx, co, nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("ivan purges versions: %v", err)
	}
	if err := s.EmptyTrash(ctx, ivan, co); !errors.Is(err, ErrForbidden) {
		t.Errorf("ivan empties the trash: %v", err)
	}
	if n, err := s.PurgeVersions(ctx, my, []string{plan}); err != nil || n != 2 {
		t.Errorf("plan's versions purged: %d %v", n, err)
	}
	if vs, _ := s.Versions(ctx, plan); len(vs) != 1 || vs[0].Size != 130 {
		t.Errorf("plan's versions = %+v", vs)
	}
	if n, err := s.PurgeVersions(ctx, my, nil); err != nil || n != 1 {
		t.Errorf("all versions purged: %d %v", n, err)
	}
	must(s.EmptyTrash(ctx, anna, my))
	if sp, _ = s.Space(ctx, my); sp.Used() != 530 || sp.TrashCount != 0 || sp.VersionCount != 0 {
		t.Errorf("space cleaned = %+v", sp)
	}

	// at most 2 former versions kept: the oldest go at a new version
	must(s.SaveLimits(ctx, Limits{Versions: 2}))
	if l, _ := s.Limits(ctx); l.Versions != 2 {
		t.Errorf("limits = %+v", l)
	}
	for i, k := range []string{"p4", "p5", "p6"} {
		must(s.AddVersion(ctx, anna, my, plan, NewVersion{Key: k, Size: int64(140 + i), ContentType: "x"}))
	}
	if vs, _ := s.Versions(ctx, plan); len(vs) != 3 || vs[0].Size != 142 || vs[1].Size != 141 || vs[2].Size != 140 {
		t.Errorf("versions kept = %+v", vs)
	}
}
