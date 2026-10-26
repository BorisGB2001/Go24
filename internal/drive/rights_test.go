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

// TestDriveFolderRights: «Права доступа» of a folder of the company's drive «Настроить права» — the folder
// is seen only by those it is open to (with its sub-departments), the folders inside have its rights unless they have
// their own, read / edit / full limit what they do, and the folder is hidden everywhere else too: search,
// «Документы», downloading, the trash. A project's managers have every folder of its drive.
func TestDriveFolderRights(t *testing.T) {
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
	admin, buh, buhChief, sales, other := newUser("admin"), newUser("buh"), newUser("buhchief"), newUser("sales"), newUser("other")
	var fin, acc string // «Финансы» and its sub-department «Бухгалтерия»
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Финансы') RETURNING id`).Scan(&fin))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Бухгалтерия', $1) RETURNING id`, fin).Scan(&acc))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4)`, buh, acc, buhChief, fin)
	must(err)
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	actx := tasks.WithAdmin(ctx)
	open := func(c context.Context, u, which string) Drive {
		t.Helper()
		d, err := s.Open(c, u, which)
		must(err)
		return d
	}
	co := open(actx, admin, "company")
	money, err := s.CreateFolder(actx, admin, co, "", "Бухгалтерия")
	must(err)
	reports, err := s.CreateFolder(actx, admin, co, money, "Отчёты")
	must(err)
	pub, err := s.CreateFolder(actx, admin, co, "", "Общее")
	must(err)
	addFile := func(folder, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, folder_id)
			VALUES (gen_random_uuid()::text, $1, 1, $2, 'company', $3) RETURNING id`, name, admin, folder).Scan(&id))
		return id
	}
	salary := addFile(reports, "зарплата.xlsx")
	addFile(pub, "правила.docx")

	// a non-administrator does not set rights where they have no full access
	if err := s.SetRight(ctx, sales, open(ctx, sales, "company"), money, "user", sales, "full"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("rights by an employee: %v", err)
	}
	// «Бухгалтерия» — to «Финансы» (with its sub-department) for edit, and to the chief for full access
	must(s.SetRight(actx, admin, co, money, "department", fin, "edit"))
	must(s.SetRight(actx, admin, co, money, "user", buhChief, "full"))

	names := func(u string) map[string]bool {
		t.Helper()
		folders, _, _, err := s.List(ctx, open(ctx, u, "company"), "")
		must(err)
		out := map[string]bool{}
		for _, f := range folders {
			out[f.Name] = true
		}
		return out
	}
	if n := names(sales); n["Бухгалтерия"] || !n["Общее"] {
		t.Fatalf("an employee of another department sees %v", n)
	}
	if n := names(buh); !n["Бухгалтерия"] {
		t.Fatalf("a sub-department of the one it is open to sees %v", n)
	}
	if folders, _, _, err := s.List(actx, co, ""); err != nil || len(folders) != 2 {
		t.Fatalf("the administrator sees every folder: %v %v", folders, err)
	}
	// a closed folder (and those within it) is not found by its address either
	if _, _, _, err := s.List(ctx, open(ctx, sales, "company"), reports); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a folder within a closed one: %v", err)
	}
	// the sub-folder has the rights of «Бухгалтерия»: edit — upload, but not delete what others made
	dBuh := open(ctx, buh, "company")
	must(s.CheckFolder(ctx, dBuh, reports))
	if err := s.DeleteFile(ctx, buh, dBuh, salary); !errors.Is(err, ErrForbidden) {
		t.Fatalf("edit access deletes another's file: %v", err)
	}
	if s.MayRight(ctx, dBuh, money) {
		t.Fatal("edit access changes the rights")
	}
	// the chief — full access: deletes anything and sets the rights
	dChief := open(ctx, buhChief, "company")
	if in, err := s.Within(ctx, dChief, reports); err != nil || !in.CanManage {
		t.Fatalf("full access: %+v %v", in, err)
	}
	// «Отчёты» of its own: read for the department — it narrows what they do
	must(s.SetRight(ctx, buhChief, dChief, reports, "department", acc, "read"))
	if err := s.CheckFolder(ctx, dBuh, reports); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read access uploads: %v", err)
	}
	if _, _, _, err := s.List(ctx, dBuh, reports); err != nil {
		t.Fatalf("read access lists: %v", err)
	}
	// the one who set the first right of a folder keeps full access to it
	if in, err := s.Within(ctx, dChief, reports); err != nil || !in.CanManage {
		t.Fatalf("the one who closed the folder: %+v %v", in, err)
	}
	r, err := s.FolderRights(ctx, []string{money, reports})
	must(err)
	if len(r[money]) != 2 || len(r[reports]) != 2 {
		t.Fatalf("rights: %+v", r)
	}
	// nor in the search, «Документы», downloading
	if _, files, err := s.Search(ctx, open(ctx, sales, "company"), "зарплата"); err != nil || len(files) != 0 {
		t.Fatalf("search of an employee it is closed to: %v %v", files, err)
	}
	if _, files, err := s.Search(ctx, dBuh, "зарплата"); err != nil || len(files) != 1 {
		t.Fatalf("search of the department: %v %v", files, err)
	}
	docs, err := s.Documents(ctx, sales, []string{"xlsx", "docx"}, DocFilter{}, 100)
	must(err)
	if len(docs) != 1 || docs[0].Name != "правила.docx" {
		t.Fatalf("«Документы» of an employee it is closed to: %+v", docs)
	}
	if s.CanReadFile(ctx, sales, salary, "company", "") || !s.CanReadFile(ctx, buh, salary, "company", "") || !s.CanReadFile(actx, admin, salary, "company", "") {
		t.Fatal("downloading a file of a closed folder")
	}
	// one who does not see all of a folder does not move or delete it with what it holds
	must(s.SetRight(actx, admin, co, pub, "all", "", "full"))
	hidden, err := s.CreateFolder(actx, admin, co, pub, "Секрет")
	must(err)
	must(s.SetRight(actx, admin, co, hidden, "user", admin, "full"))
	dSales := open(ctx, sales, "company")
	if err := s.DeleteFolder(ctx, sales, dSales, pub); !errors.Is(err, ErrForbidden) {
		t.Fatalf("deleting a folder with a hidden one inside: %v", err)
	}
	// the trash shows nothing of a closed folder
	must(s.DeleteFile(actx, admin, co, salary))
	trash, err := s.Trash(ctx, dSales)
	must(err)
	if len(trash) != 0 {
		t.Fatalf("the trash of an employee it is closed to: %+v", trash)
	}
	if trash, err = s.Trash(ctx, dBuh); err != nil || len(trash) != 1 {
		t.Fatalf("the trash of the department: %+v %v", trash, err)
	}
	// with no rights left the folder is the drive's again
	must(s.ClearRights(actx, co, money))
	if n := names(sales); !n["Бухгалтерия"] {
		t.Fatalf("a folder without rights: %v", n)
	}
	// one may not take their own full access away
	must(s.SetRight(actx, admin, co, money, "user", buhChief, "full"))
	must(s.SetRight(ctx, buhChief, dChief, money, "user", other, "read"))
	rs, err := s.FolderRights(ctx, []string{money})
	must(err)
	for _, x := range rs[money] {
		if x.WhomID == buhChief {
			if err := s.DeleteRight(ctx, buhChief, dChief, money, x.ID); !errors.Is(err, ErrForbidden) {
				t.Fatalf("taking one's own access away: %v", err)
			}
		}
	}

	// a project's drive: its managers have every folder, a member — only those open to them
	_, err = ts.CreateProject(ctx, admin, "PR", "Проект", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, admin, "PR")
	must(ts.SetMember(ctx, p, sales, "member"))
	must(ts.SetMember(ctx, p, other, "manager"))
	pd := open(ctx, admin, "PR")
	closed, err := s.CreateFolder(ctx, admin, pd, "", "Договоры")
	must(err)
	must(s.SetRight(ctx, admin, pd, closed, "user", admin, "full"))
	if folders, _, _, err := s.List(ctx, open(ctx, sales, "PR"), ""); err != nil || len(folders) != 0 {
		t.Fatalf("a member of the project: %v %v", folders, err)
	}
	if folders, _, _, err := s.List(ctx, open(ctx, other, "PR"), ""); err != nil || len(folders) != 1 {
		t.Fatalf("a manager of the project: %v %v", folders, err)
	}
	// «Мой диск» has no folder rights
	if s.MayRight(ctx, open(ctx, admin, "my"), "") {
		t.Fatal("rights on «Мой диск»")
	}
}
