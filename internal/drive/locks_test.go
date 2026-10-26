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

// TestDriveLocks: «Заблокировать» — while Anna has her file locked, the administrator may not change, rename, move or
// delete it nor the folder holding it; Anna may; the administrator may lift the lock, Ivan may not.
func TestDriveLocks(t *testing.T) {
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
	var anna, ivan, boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'i@t') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Админ', 'b@t') RETURNING id`).Scan(&boss))
	s := NewStore(pool, tasks.NewStore(pool))
	co := Drive{Kind: "company", CanWrite: true}
	admin := Drive{Kind: "company", CanWrite: true, CanManage: true}
	folder, err := s.CreateFolder(ctx, anna, co, "", "Договоры")
	must(err)
	var f string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, folder_id)
		VALUES ('k1', 'договор.docx', 10, $1, 'company', $2) RETURNING id`, anna, folder).Scan(&f))

	if err := s.Lock(ctx, ivan, co, f); !errors.Is(err, ErrForbidden) {
		t.Errorf("ivan locks anna's file: %v", err)
	}
	must(s.Lock(ctx, anna, co, f))
	_, files, _, err := s.List(ctx, co, folder)
	must(err)
	if len(files) != 1 || files[0].LockerID != anna || files[0].Locker != "Анна" || files[0].LockedAt == nil {
		t.Fatalf("the listed file = %+v", files)
	}
	locked := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrLocked) {
			t.Errorf("%s: %v, want locked", what, err)
		}
	}
	locked("a version", s.AddVersion(ctx, boss, admin, f, NewVersion{Key: "k2", Size: 5}))
	locked("rename", s.RenameFile(ctx, boss, admin, f, "x.docx"))
	locked("move", s.Move(ctx, boss, admin, Selection{Files: []string{f}}, admin, ""))
	locked("delete", s.DeleteFile(ctx, boss, admin, f))
	locked("the folder's delete", s.DeleteFolder(ctx, boss, admin, folder))
	locked("the folder's move", s.Move(ctx, boss, admin, Selection{Folders: []string{folder}}, Drive{Kind: "user", OwnerID: boss, CanWrite: true, CanManage: true}, ""))
	if may, err := s.MayChange(ctx, boss, admin, f); may || err != nil {
		t.Errorf("the admin may change the locked file: %v %v", may, err)
	}
	if may, err := s.MayChange(ctx, anna, co, f); !may || err != nil {
		t.Errorf("anna may not change her locked file: %v %v", may, err)
	}
	must(s.AddVersion(ctx, anna, co, f, NewVersion{Key: "k3", Size: 7}))
	must(s.RenameFile(ctx, anna, co, f, "договор 2.docx"))

	if err := s.Unlock(ctx, ivan, co, f); !errors.Is(err, ErrForbidden) {
		t.Errorf("ivan unlocks: %v", err)
	}
	must(s.Unlock(ctx, boss, admin, f))
	must(s.RenameFile(ctx, boss, admin, f, "договор 3.docx"))
	must(s.Lock(ctx, anna, co, f))
	must(s.Unlock(ctx, anna, co, f))
	must(s.Unlock(ctx, anna, co, f)) // not locked: nothing to do

	// who has it open in the editor: shown in the list until everyone closes it
	must(s.Editing(ctx, f, []string{ivan, anna, "x"}))
	if _, files, _, _ := s.List(ctx, co, folder); files[0].Editors != "Анна, Иван" {
		t.Errorf("editors = %q", files[0].Editors)
	}
	must(s.Editing(ctx, f, nil))
	if _, files, _, _ := s.List(ctx, co, folder); files[0].Editors != "" {
		t.Errorf("editors after closing = %q", files[0].Editors)
	}
	must(s.DeleteFolder(ctx, boss, admin, folder))
}
