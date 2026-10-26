package drive

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestDriveSharing: «Общий доступ» of a file — its own shares and link, the folders above it that open it (shares
// and links of each, «Права доступа» only of the nearest folder with rights of its own).
func TestDriveSharing(t *testing.T) {
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	anna, ivan, olga := newUser("anna"), newUser("ivan"), newUser("olga")
	s := NewStore(pool, tasks.NewStore(pool))
	my, err := s.Open(ctx, anna, "my")
	must(err)
	work, err := s.CreateFolder(ctx, anna, my, "", "Работа")
	must(err)
	quiet, err := s.CreateFolder(ctx, anna, my, work, "Тихая")
	must(err)
	reports, err := s.CreateFolder(ctx, anna, my, quiet, "Отчёты")
	must(err)
	var plan string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id)
		VALUES (gen_random_uuid()::text, 'план.docx', 3, $1, 'user', $1, $2) RETURNING id`, anna, reports).Scan(&plan))

	sh, err := s.Sharing(ctx, plan)
	must(err)
	if len(sh.Shares) != 0 || sh.Link != nil || len(sh.Folders) != 0 || sh.Ruled() {
		t.Fatalf("a file shared with nobody: %+v", sh)
	}
	must(s.SetShare(ctx, anna, my, work, "", "user", ivan, "edit"))
	must(s.SetShare(ctx, anna, my, "", plan, "user", olga, "read"))
	_, err = s.SetLink(ctx, anna, my, "folder", reports, "", false, nil, false, "")
	must(err)
	_, err = s.SetLink(ctx, anna, my, "file", plan, "секрет", false, nil, false, "")
	must(err)
	sh, err = s.Sharing(ctx, plan)
	must(err)
	if len(sh.Shares) != 1 || sh.Shares[0].WhomID != olga || sh.Shares[0].Access != "read" {
		t.Fatalf("the file's own shares: %+v", sh.Shares)
	}
	if sh.Link == nil || !sh.Link.Password {
		t.Fatalf("the file's link: %+v", sh.Link)
	}
	// «Тихая» opens it to nobody: not there; from the top down
	if len(sh.Folders) != 2 || sh.Folders[0].Name != "Работа" || sh.Folders[1].Name != "Отчёты" {
		t.Fatalf("the folders that open it: %+v", sh.Folders)
	}
	if f := sh.Folders[0]; len(f.Shares) != 1 || f.Shares[0].Whom != "ivan" || f.Shares[0].Access != "edit" || f.Link != nil {
		t.Fatalf("«Работа»: %+v", f)
	}
	if f := sh.Folders[1]; len(f.Shares) != 0 || f.Link == nil || f.Link.Password {
		t.Fatalf("«Отчёты»: %+v", f)
	}

	// the company's drive: the nearest folder with rights of its own decides
	actx := tasks.WithAdmin(ctx)
	co, err := s.Open(actx, anna, "company")
	must(err)
	top, err := s.CreateFolder(actx, anna, co, "", "Бухгалтерия")
	must(err)
	inner, err := s.CreateFolder(actx, anna, co, top, "Зарплата")
	must(err)
	var salary string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, folder_id)
		VALUES (gen_random_uuid()::text, 'зарплата.xlsx', 1, $1, 'company', $2) RETURNING id`, anna, inner).Scan(&salary))
	sh, err = s.Sharing(ctx, salary)
	must(err)
	if sh.Ruled() || len(sh.Folders) != 0 {
		t.Fatalf("no rights — every employee: %+v", sh)
	}
	must(s.SetRight(actx, anna, co, top, "all", "", "read"))
	must(s.SetRight(actx, anna, co, inner, "user", ivan, "full"))
	sh, err = s.Sharing(ctx, salary)
	must(err)
	if !sh.Ruled() || len(sh.Folders) != 1 || sh.Folders[0].Name != "Зарплата" {
		t.Fatalf("only «Зарплата» decides: %+v", sh.Folders)
	}
	got := map[string]string{}
	for _, r := range sh.Folders[0].Rights {
		got[r.Kind+":"+r.WhomID] = r.Access
	}
	if got["user:"+ivan] != "full" || len(got) < 1 {
		t.Fatalf("the rights of «Зарплата»: %+v", sh.Folders[0].Rights)
	}
}
