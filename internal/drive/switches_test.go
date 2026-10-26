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

// TestDriveSwitches: «Настройки → Диск» — with public links off no link is made, those made do not open and are not
// shown; with locking off nothing is locked and Anna's lock holds no more (until it is on again); with the search by
// the text off files are found by the name only and no text is read.
func TestDriveSwitches(t *testing.T) {
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
	var anna, boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Админ', 'b@t') RETURNING id`).Scan(&boss))
	s := NewStore(pool, tasks.NewStore(pool))
	co := Drive{Kind: "company", CanWrite: true}
	admin := Drive{Kind: "company", CanWrite: true, CanManage: true}
	folder, err := s.CreateFolder(ctx, anna, co, "", "Договоры")
	must(err)
	var f, g string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, folder_id)
		VALUES ('k1', 'договор.docx', 10, $1, 'company', $2) RETURNING id`, anna, folder).Scan(&f))
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, folder_id)
		VALUES ('k2', 'план.docx', 10, $1, 'company', $2) RETURNING id`, anna, folder).Scan(&g))
	must(s.SetContent(ctx, f, "k1", "поставки оборудования"))
	token, err := s.SetLink(ctx, anna, co, "file", f, "", false, nil)
	must(err)
	must(s.Lock(ctx, anna, co, f))

	l, err := s.Limits(ctx)
	must(err)
	if l.NoLinks || l.NoLocks || l.NoContent {
		t.Fatalf("switched off by default: %+v", l)
	}
	l.NoLinks, l.NoLocks, l.NoContent = true, true, true
	must(s.SaveLimits(ctx, l))
	if l2, _ := s.Limits(ctx); l2 != l {
		t.Fatalf("limits = %+v, want %+v", l2, l)
	}

	// public links
	if _, err := s.SetLink(ctx, anna, co, "file", g, "", false, nil); !errors.Is(err, ErrLinksOff) {
		t.Errorf("a link with links off: %v", err)
	}
	if _, err := s.OpenLink(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("the link opens with links off: %v", err)
	}
	if links, err := s.Links(ctx, []string{f}); err != nil || len(links) != 0 {
		t.Errorf("links shown with links off: %v %v", links, err)
	}
	// locks
	if err := s.Lock(ctx, anna, co, g); !errors.Is(err, ErrLocksOff) {
		t.Errorf("a lock with locks off: %v", err)
	}
	if _, files, _, _ := s.List(ctx, co, folder); files[0].LockerID != "" || files[0].Locker != "" || files[0].LockedAt != nil {
		t.Errorf("the lock is shown with locks off: %+v", files[0])
	}
	if may, err := s.MayChange(ctx, boss, admin, f); !may || err != nil {
		t.Errorf("the admin may not change the file with locks off: %v %v", may, err)
	}
	must(s.RenameFile(ctx, boss, admin, f, "договор 2.docx"))
	// the search by the text
	if _, files, _ := s.Search(ctx, co, "постав"); len(files) != 0 {
		t.Errorf("found by the text with the search off: %+v", files)
	}
	if _, files, _ := s.Search(ctx, co, "план"); len(files) != 1 {
		t.Errorf("by the name: %+v", files)
	}
	if list, err := s.Unindexed(ctx, 10); err != nil || len(list) != 0 {
		t.Errorf("to be read with the search off: %v %v", list, err)
	}

	// all on again: the link opens, Anna's lock holds, the text is found
	must(s.SaveLimits(ctx, Limits{}))
	if _, err := s.OpenLink(ctx, token); err != nil {
		t.Errorf("the link with links on: %v", err)
	}
	if err := s.RenameFile(ctx, boss, admin, f, "договор 3.docx"); !errors.Is(err, ErrLocked) {
		t.Errorf("the lock with locks on: %v", err)
	}
	if err := s.DeleteFolder(ctx, boss, admin, folder); !errors.Is(err, ErrLocked) {
		t.Errorf("the folder with a locked file: %v", err)
	}
	if _, files, _ := s.Search(ctx, co, "постав"); len(files) != 1 {
		t.Errorf("by the text with the search on: %+v", files)
	}
	if list, _ := s.Unindexed(ctx, 10); len(list) != 1 || list[0].ID != g {
		t.Errorf("to be read: %+v", list)
	}
}
