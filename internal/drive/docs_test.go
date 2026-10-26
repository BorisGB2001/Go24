package drive

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestDriveDocuments: «Документы» — the documents of one's drive, the company's, one's projects' and the folders
// shared with one (with the share as the drive to open them from); not others', not trashed, not other files.
func TestDriveDocuments(t *testing.T) {
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
	var anna, ivan, guest string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'i@t') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ('g', 'Гость', 'g@t', true) RETURNING id`).Scan(&guest))
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "DOC", "Проект", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, anna, "DOC")
	s := NewStore(pool, ts)
	my, _ := s.Open(ctx, anna, "my")
	shared, err := s.CreateFolder(ctx, anna, my, "", "Общая")
	must(err)
	inner, err := s.CreateFolder(ctx, anna, my, shared, "Внутри")
	must(err)
	private, err := s.CreateFolder(ctx, anna, my, "", "Личное")
	must(err)
	must(s.SetShare(ctx, anna, my, shared, "", "user", ivan, "read"))
	add := func(by, drive, owner, folder, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id)
			VALUES (gen_random_uuid()::text, $1, 3, $2, $3, $4, $5) RETURNING id`, name, by, drive, nullable(owner), nullable(folder)).Scan(&id))
		return id
	}
	plan := add(anna, "user", anna, inner, "план.docx")
	add(anna, "user", anna, private, "личное.docx")
	add(anna, "user", anna, "", "фото.jpg")
	budget := add(ivan, "company", "", "", "Бюджет.XLSX")
	slides := add(anna, "project", p.ID, "", "отчёт.pptx")
	gone := add(ivan, "user", ivan, "", "старое.docx")
	_, err = pool.Exec(ctx, `UPDATE files SET deleted_at = now() WHERE id = $1`, gone)
	must(err)
	exts := []string{"docx", "xlsx", "pptx"}

	names := func(list []Document) []string {
		var out []string
		for _, d := range list {
			out = append(out, d.Name+" "+d.Base)
		}
		slices.Sort(out)
		return out
	}
	list, err := s.Documents(ctx, anna, exts, DocFilter{}, 100)
	must(err)
	if got := names(list); !slices.Equal(got, []string{"Бюджет.XLSX /disk/company", "личное.docx /disk/my", "отчёт.pptx /disk/p/DOC", "план.docx /disk/my"}) {
		t.Errorf("anna's documents = %q", got)
	}
	list, err = s.Documents(ctx, ivan, exts, DocFilter{}, 100)
	must(err)
	if got := names(list); !slices.Equal(got, []string{"Бюджет.XLSX /disk/company", "план.docx /disk/sh/" + shared}) {
		t.Errorf("ivan's documents = %q", got)
	}
	for _, d := range list {
		if d.ID == plan && (d.Owner != "Анна" || d.FolderID != inner || d.Where != "Внутри" || d.Changer != "Анна") {
			t.Errorf("the shared document = %+v", d)
		}
	}
	if list, _ = s.Documents(ctx, ivan, exts, DocFilter{Scope: "mine"}, 100); len(list) != 1 || list[0].ID != budget {
		t.Errorf("ivan's own documents = %+v", list)
	}
	if list, _ = s.Documents(ctx, ivan, exts, DocFilter{Scope: "shared"}, 100); len(list) != 1 || list[0].ID != plan {
		t.Errorf("documents shared with ivan = %+v", list)
	}
	if list, _ = s.Documents(ctx, anna, exts, DocFilter{Exts: []string{"pptx"}}, 100); len(list) != 1 || list[0].ID != slides || list[0].ProjectName != "Проект" {
		t.Errorf("anna's presentations = %+v", list)
	}
	if list, _ = s.Documents(ctx, anna, exts, DocFilter{Q: "бюдж"}, 100); len(list) != 1 || list[0].ID != budget {
		t.Errorf("anna finds «бюдж»: %+v", list)
	}
	// the shared folder in the trash: nothing of it for ivan; a guest has no company drive
	_, err = pool.Exec(ctx, `UPDATE drive_folders SET deleted_at = now() WHERE id = $1`, shared)
	must(err)
	if list, _ = s.Documents(ctx, ivan, exts, DocFilter{}, 100); len(list) != 1 || list[0].ID != budget {
		t.Errorf("ivan's documents with the folder trashed = %+v", list)
	}
	if list, _ = s.Documents(ctx, guest, exts, DocFilter{}, 100); len(list) != 0 {
		t.Errorf("the guest's documents = %+v", list)
	}
}
