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

// TestDriveFileShares: «Поделиться» a single file of «Мой диск» — it is in «Общие со мной» and «Документы» of those
// it is shared with and opens as a drive of that file alone: read, or edit the file itself, nothing else of the drive.
func TestDriveFileShares(t *testing.T) {
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
	var anna, ivan, olga, sales string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'i@t') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('olga', 'Ольга', 'o@t') RETURNING id`).Scan(&olga))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, olga, sales)
	must(err)
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	my, _ := s.Open(ctx, anna, "my")
	folder, err := s.CreateFolder(ctx, anna, my, "", "Работа")
	must(err)
	add := func(name, folder string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id)
			VALUES (gen_random_uuid()::text, $1, 3, $2, 'user', $2, $3) RETURNING id`, name, anna, nullable(folder)).Scan(&id))
		return id
	}
	plan := add("план.docx", folder)
	other := add("другое.docx", folder)

	// only the owner shares, with the accesses of a file
	if err := s.SetShare(ctx, anna, my, "", plan, "user", ivan, "full"); !errors.Is(err, ErrNotFound) {
		t.Errorf("full access to a file: %v", err)
	}
	ivanMy, _ := s.Open(ctx, ivan, "my")
	if err := s.SetShare(ctx, ivan, ivanMy, "", plan, "user", olga, "read"); !errors.Is(err, ErrNotFound) {
		t.Errorf("sharing another's file: %v", err)
	}
	must(s.SetShare(ctx, anna, my, "", plan, "user", ivan, "read"))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'disk_file_share' AND url = $2`, ivan, "/disk/sf/"+plan).Scan(&n))
	if n != 1 {
		t.Errorf("notifications of the share: %d", n)
	}
	shares, _ := s.Shares(ctx, []string{plan, folder})
	if len(shares[plan]) != 1 || shares[plan][0].Whom != "Иван" || len(shares[folder]) != 0 {
		t.Errorf("shares = %+v", shares)
	}
	list, err := s.SharedWithMe(ctx, ivan)
	must(err)
	if len(list) != 1 || list[0].ID != plan || !list[0].File || list[0].Owner != "Анна" || list[0].Access != "read" {
		t.Errorf("shared with ivan = %+v", list)
	}
	docs, _ := s.Documents(ctx, ivan, []string{"docx"}, DocFilter{}, 10)
	if len(docs) != 1 || docs[0].ID != plan || docs[0].Base != "/disk/sf/"+plan || !docs[0].Single || docs[0].Owner != "Анна" {
		t.Errorf("ivan's documents = %+v", docs)
	}
	if !s.SharedFileTo(ctx, ivan, plan) || s.SharedFileTo(ctx, ivan, other) || s.SharedFileTo(ctx, olga, plan) {
		t.Error("SharedFileTo: only the file shared, only to ivan")
	}

	// the drive of the file: read — no change; nothing else of anna's drive in it
	d, err := s.Open(ctx, ivan, "sf:"+plan)
	must(err)
	if d.Path() != "/disk/sf/"+plan || d.Owner != "Анна" || d.Name != "план.docx" || d.CanWrite || d.CanManage {
		t.Errorf("the drive of the file = %+v", d)
	}
	if may, err := s.MayChange(ctx, ivan, d, plan); may || err != nil {
		t.Errorf("ivan may change the read file: %v %v", may, err)
	}
	if _, err := s.MayChange(ctx, ivan, d, other); !errors.Is(err, ErrNotFound) {
		t.Errorf("another file through the shared one: %v", err)
	}
	if _, err := s.Open(ctx, olga, "sf:"+plan); !errors.Is(err, ErrNotFound) {
		t.Errorf("olga opens the file: %v", err)
	}
	if _, err := s.Open(ctx, anna, "sf:"+plan); !errors.Is(err, ErrNotFound) {
		t.Errorf("the owner opens it as shared: %v", err)
	}

	// edit: the file itself changes, but it is not renamed, deleted or moved, and nothing is put beside it
	must(s.SetShare(ctx, anna, my, "", plan, "user", ivan, "edit"))
	d, _ = s.Open(ctx, ivan, "sf:"+plan)
	if may, err := s.MayChange(ctx, ivan, d, plan); !may || err != nil {
		t.Errorf("ivan may not edit the file: %v %v", may, err)
	}
	if err := s.RenameFile(ctx, ivan, d, plan, "моё.docx"); !errors.Is(err, ErrForbidden) {
		t.Errorf("rename: %v", err)
	}
	if err := s.DeleteFile(ctx, ivan, d, plan); !errors.Is(err, ErrForbidden) {
		t.Errorf("delete: %v", err)
	}
	if err := s.Move(ctx, ivan, d, Selection{Files: []string{plan}}, ivanMy, ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("move: %v", err)
	}
	if err := s.CheckFolder(ctx, d, ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("upload beside the file: %v", err)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'disk_file_share'`, ivan).Scan(&n))
	if n != 1 {
		t.Errorf("a changed access notifies again: %d", n)
	}

	// a department: the ones in it
	must(s.SetShare(ctx, anna, my, "", plan, "department", sales, "read"))
	if d, err := s.Open(ctx, olga, "sf:"+plan); err != nil || d.Access != "read" {
		t.Errorf("olga through the department: %+v %v", d, err)
	}

	// the folder in the trash — the file is not shared any more; closed — neither
	_, err = pool.Exec(ctx, `UPDATE drive_folders SET deleted_at = now() WHERE id = $1`, folder)
	must(err)
	if list, _ := s.SharedWithMe(ctx, ivan); len(list) != 0 {
		t.Errorf("shared with the folder trashed = %+v", list)
	}
	if _, err := s.Open(ctx, ivan, "sf:"+plan); !errors.Is(err, ErrNotFound) {
		t.Errorf("open with the folder trashed: %v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE drive_folders SET deleted_at = NULL WHERE id = $1`, folder)
	must(err)
	shares, _ = s.Shares(ctx, []string{plan})
	for _, sh := range shares[plan] {
		if sh.Kind == "user" {
			if err := s.DeleteShare(ctx, ivan, ivanMy, sh.ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("ivan closes anna's share: %v", err)
			}
			must(s.DeleteShare(ctx, anna, my, sh.ID))
		}
	}
	if _, err := s.Open(ctx, ivan, "sf:"+plan); !errors.Is(err, ErrNotFound) {
		t.Errorf("open after the share is closed: %v", err)
	}
}
