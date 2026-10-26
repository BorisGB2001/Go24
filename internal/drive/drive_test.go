package drive

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestDrive: my drive, the company drive and a project drive; folders, who writes and deletes, who reads.
func TestDrive(t *testing.T) {
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
	boss, dev, guest := newUser("boss"), newUser("dev"), newUser("guest")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "DR", "Проект", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, boss, "DR")
	must(ts.SetMember(ctx, p, dev, "viewer"))
	s := NewStore(pool, ts)
	addFile := func(by, drive, owner, folder, name string) string {
		var id string
		var o, f *string
		if owner != "" {
			o = &owner
		}
		if folder != "" {
			f = &folder
		}
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id)
			VALUES ($1, $1, 1, $2, $3, $4, $5) RETURNING id`, name, by, drive, o, f).Scan(&id))
		return id
	}

	co, _ := s.Open(ctx, guest, "company")
	docs, err := s.CreateFolder(ctx, guest, co, "", "Документы")
	must(err)
	var ve *i18n.Error
	if _, err := s.CreateFolder(ctx, boss, co, "", "документы"); !errors.As(err, &ve) {
		t.Fatalf("the same folder twice: %v", err)
	}
	sub, err := s.CreateFolder(ctx, boss, co, docs, "Договоры")
	must(err)
	f := addFile(guest, "company", "", sub, "договор.pdf")
	folders, files, trail, err := s.List(ctx, co, sub)
	must(err)
	if len(folders) != 0 || len(files) != 1 || len(trail) != 2 || trail[0].ID != docs {
		t.Fatalf("company/Документы/Договоры: %v %v %v", folders, files, trail)
	}
	coBoss, _ := s.Open(ctx, boss, "company")
	if err := s.DeleteFile(ctx, boss, coBoss, f); !errors.Is(err, ErrForbidden) {
		t.Fatalf("someone else's file deleted on the company drive: %v", err)
	}
	if err := s.DeleteFolder(ctx, guest, co, docs); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a folder with someone else's folder inside deleted by its maker: %v", err)
	}
	must(s.DeleteFile(ctx, guest, co, f))
	must(s.DeleteFolder(ctx, boss, coBoss, sub)) // its maker
	if !s.CanRead(ctx, dev, "company", "") {
		t.Fatal("the company drive is everyone's")
	}

	// a project drive: a viewer reads, does not write; a stranger does not even open it
	pd, err := s.Open(ctx, dev, "dr")
	must(err)
	if _, err := s.CreateFolder(ctx, dev, pd, "", "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a viewer made a folder: %v", err)
	}
	if _, err := s.Open(ctx, guest, "DR"); err == nil {
		t.Fatal("a stranger opened the project drive")
	}
	if !s.CanRead(ctx, dev, "project", p.ID) || s.CanRead(ctx, guest, "project", p.ID) {
		t.Fatal("project drive reading")
	}
	// my drive: mine only; a folder of another drive is not found in it
	my, _ := s.Open(ctx, dev, "my")
	if _, _, _, err := s.List(ctx, my, docs); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a company folder listed in my drive: %v", err)
	}
	if s.CanRead(ctx, guest, "user", dev) {
		t.Fatal("someone else's drive is readable")
	}

	// a collab's drive: its members (a guest from outside too) write in it; nobody else opens it, lists it or reads
	// its files — not even by the drive's id
	var client string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ('client', 'client', 'c@t', true) RETURNING id`).Scan(&client))
	ckey, err := ts.CreateCollab(ctx, boss, "Клиент", "", []string{client})
	must(err)
	cd, err := s.Open(ctx, client, ckey)
	must(err)
	if !cd.CanWrite || cd.CanManage || cd.Path() != "/disk/p/"+ckey {
		t.Fatalf("the guest's collab drive = %+v", cd)
	}
	cf, err := s.CreateFolder(ctx, client, cd, "", "Макеты")
	must(err)
	addFile(client, "project", cd.OwnerID, cf, "макет.png")
	for name, u := range map[string]string{"employee": dev, "stranger": guest} {
		if _, err := s.Open(ctx, u, ckey); !errors.Is(err, tasks.ErrNotFound) {
			t.Errorf("%s opened the collab drive: %v", name, err)
		}
		if s.CanRead(ctx, u, "project", cd.OwnerID) {
			t.Errorf("%s reads the collab drive's files", name)
		}
	}
	if folders, files, _, err := s.List(ctx, cd, cf); err != nil || len(folders) != 0 || len(files) != 1 {
		t.Fatalf("the collab drive: %v %v %v", folders, files, err)
	}
}

// TestDriveActions: renaming, moving within a drive and to another one (a folder with all it holds), copying with
// «name (2)», the archive of a folder.
func TestDriveActions(t *testing.T) {
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
	anna, ivan := newUser("anna"), newUser("ivan")
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	my, _ := s.Open(ctx, anna, "my")
	co, _ := s.Open(ctx, anna, "company")
	coIvan, _ := s.Open(ctx, ivan, "company")
	addFile := func(d Drive, folder, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id)
			VALUES (gen_random_uuid()::text, $1, 3, $2, $3, $4, $5) RETURNING id`, name, anna, d.Kind, d.owner(), nullable(folder)).Scan(&id))
		return id
	}
	docs, err := s.CreateFolder(ctx, anna, my, "", "Документы")
	must(err)
	sub, err := s.CreateFolder(ctx, anna, my, docs, "Сканы")
	must(err)
	f1 := addFile(my, docs, "отчёт.docx")
	addFile(my, sub, "скан.pdf")

	// rename
	must(s.RenameFile(ctx, anna, my, f1, " итог.docx "))
	if err := s.RenameFolder(ctx, anna, my, sub, "документы"); err != nil {
		t.Fatalf("a folder named as its parent: %v", err)
	}
	must(s.RenameFolder(ctx, anna, my, sub, "Сканы 2026"))
	var ve *i18n.Error
	if err := s.RenameFile(ctx, anna, my, f1, "a/b"); !errors.As(err, &ve) {
		t.Fatalf("a slash in a file name: %v", err)
	}
	cf := addFile(co, "", "общий.txt")
	if err := s.RenameFile(ctx, ivan, coIvan, cf, "чужой.txt"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("someone else's file renamed: %v", err)
	}

	// a folder cannot go into itself or below it
	if err := s.Move(ctx, anna, my, Selection{Folders: []string{docs}}, my, sub); !errors.As(err, &ve) {
		t.Fatalf("a folder moved into its subfolder: %v", err)
	}
	// a file to the top, then the whole folder to the company drive — what it holds goes too
	must(s.Move(ctx, anna, my, Selection{Files: []string{f1}}, my, ""))
	must(s.Move(ctx, anna, my, Selection{Folders: []string{docs}}, co, ""))
	folders, files, _, err := s.List(ctx, co, docs)
	must(err)
	if len(folders) != 1 || len(files) != 0 {
		t.Fatalf("the moved folder on the company drive: %v %v", folders, files)
	}
	if _, files, _, _ := s.List(ctx, co, sub); len(files) != 1 || files[0].Name != "скан.pdf" {
		t.Fatalf("the subfolder's file did not follow: %v", files)
	}
	if _, _, _, err := s.List(ctx, my, docs); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the moved folder is still in my drive: %v", err)
	}
	if !s.CanRead(ctx, ivan, "company", "") {
		t.Fatal("company drive")
	}
	// someone else may not move it out of the company drive
	if err := s.Move(ctx, ivan, coIvan, Selection{Folders: []string{docs}}, coIvan, sub); !errors.Is(err, ErrForbidden) && !errors.As(err, &ve) {
		t.Fatalf("someone else's folder moved: %v", err)
	}

	// copies: the bytes are copied under a new key; a second copy into the same place is «name (2)»
	var copied []string
	cp := func(_ context.Context, _ *string, from, to string, _ int64, _ string) error {
		if from == to {
			t.Fatal("the same key")
		}
		copied = append(copied, to)
		return nil
	}
	must(s.Copy(ctx, anna, my, Selection{Files: []string{f1}}, my, "", cp))
	must(s.Copy(ctx, anna, co, Selection{Folders: []string{docs}}, my, "", cp))
	must(s.Copy(ctx, anna, co, Selection{Folders: []string{docs}}, my, "", cp))
	folders, files, _, err = s.List(ctx, my, "")
	must(err)
	names := []string{}
	for _, f := range folders {
		names = append(names, f.Name)
	}
	for _, f := range files {
		names = append(names, f.Name)
	}
	if want := "Документы Документы (2) итог (2).docx итог.docx"; strings.Join(names, " ") != want {
		t.Fatalf("after copying: %q, want %q", strings.Join(names, " "), want)
	}
	if len(copied) != 3 {
		t.Fatalf("bytes copied %d times", len(copied))
	}

	// the archive of a folder: paths inside it
	entries, err := s.Archive(ctx, co, Selection{Folders: []string{docs}, Files: []string{cf}})
	must(err)
	paths := []string{}
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	if want := "общий.txt Документы/Сканы 2026/скан.pdf"; strings.Join(paths, " ") != want {
		t.Fatalf("archive: %q", strings.Join(paths, " "))
	}
	if _, err := s.Archive(ctx, my, Selection{Folders: []string{sub}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another drive's folder archived: %v", err)
	}
}

func TestFreeName(t *testing.T) {
	for _, c := range []struct {
		name  string
		taken []string
		file  bool
		want  string
	}{
		{"a.txt", nil, true, "a.txt"},
		{"a.txt", []string{"a.txt"}, true, "a (2).txt"},
		{"A.txt", []string{"a.txt", "a (2).txt"}, true, "A (3).txt"},
		{"v1.2", []string{"v1.2"}, false, "v1.2 (2)"},
		{".env", []string{".env"}, true, ".env (2)"},
	} {
		if got := freeName(c.name, c.taken, c.file); got != c.want {
			t.Errorf("freeName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDriveTrash: deleting puts files and folders (with all they hold) into the drive's trash, out of the lists; a
// new folder may take the name of one in the trash; restoring brings them back (to the top when their folder is
// gone, «name (2)» on a namesake); deleting for good and the expiry of the company's setting.
func TestDriveTrash(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'ivan', 'i@t') RETURNING id`).Scan(&ivan))
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	co, _ := s.Open(ctx, anna, "company")
	coIvan, _ := s.Open(ctx, ivan, "company")
	addFile := func(by, folder, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, folder_id)
			VALUES (gen_random_uuid()::text, $1, 3, $2, 'company', $3) RETURNING id`, name, by, nullable(folder)).Scan(&id))
		return id
	}
	docs, err := s.CreateFolder(ctx, anna, co, "", "Документы")
	must(err)
	sub, err := s.CreateFolder(ctx, anna, co, docs, "Сканы")
	must(err)
	inSub := addFile(anna, sub, "скан.pdf")
	loose := addFile(anna, docs, "отчёт.docx")
	theirs := addFile(ivan, docs, "чужой.txt")

	// a folder with someone else's file inside is not the maker's to delete; the file of the folder is
	if err := s.DeleteFolder(ctx, anna, co, docs); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a folder with ivan's file deleted by anna: %v", err)
	}
	must(s.DeleteFile(ctx, anna, co, loose))
	must(s.DeleteFolder(ctx, anna, co, sub))
	if _, files, _, _ := s.List(ctx, co, docs); len(files) != 1 || files[0].ID != theirs {
		t.Fatalf("the folder after deleting: %v", files)
	}
	if folders, _, _, _ := s.List(ctx, co, docs); len(folders) != 0 {
		t.Fatalf("a deleted folder is listed: %v", folders)
	}
	if _, _, _, err := s.List(ctx, co, sub); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted folder opens: %v", err)
	}
	if err := s.RenameFile(ctx, anna, co, inSub, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a file in a deleted folder renamed: %v", err)
	}
	trash, err := s.Trash(ctx, co)
	must(err)
	if len(trash) != 2 || trash[0].PurgeAt == nil {
		t.Fatalf("trash: %+v", trash)
	}
	// ivan neither restores nor purges what anna deleted
	if err := s.Restore(ctx, ivan, coIvan, Selection{Files: []string{loose}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ivan restored anna's file: %v", err)
	}
	// a new «Сканы» while the old one is in the trash; restoring the old one makes it «Сканы (2)»
	if _, err := s.CreateFolder(ctx, anna, co, docs, "сканы"); err != nil {
		t.Fatalf("a folder named as one in the trash: %v", err)
	}
	must(s.Restore(ctx, anna, co, Selection{Folders: []string{sub}}))
	if folders, _, _, _ := s.List(ctx, co, docs); len(folders) != 2 || folders[1].Name != "Сканы (2)" {
		t.Fatalf("restored next to a namesake: %v", folders)
	}
	if _, files, _, _ := s.List(ctx, co, sub); len(files) != 1 {
		t.Fatalf("the restored folder's file: %v", files)
	}
	// the file's folder goes to the trash, then the file comes back — to the top
	coBoss := co
	coBoss.CanManage = true
	must(s.DeleteFolder(ctx, anna, coBoss, docs))
	must(s.Restore(ctx, anna, co, Selection{Files: []string{loose}}))
	if _, files, _, _ := s.List(ctx, co, ""); len(files) != 1 || files[0].ID != loose {
		t.Fatalf("a file whose folder is in the trash restored at the top: %v", files)
	}
	// deleting for good: the folder with all it holds
	must(s.Purge(ctx, anna, co, Selection{Folders: []string{docs}}))
	var left int
	must(pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM files) + (SELECT count(*) FROM drive_folders)`).Scan(&left))
	if left != 1 {
		t.Fatalf("after purging the folder %d rows are left, want the restored file only", left)
	}
	// expiry
	must(s.DeleteFile(ctx, anna, co, loose))
	if n, err := s.PurgeExpired(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("purged too early: %d %v", n, err)
	}
	if n, err := s.PurgeExpired(ctx, time.Now().AddDate(0, 0, 400)); err != nil || n != 1 {
		t.Fatalf("expired: %d %v", n, err)
	}
}

// TestDriveVersions: uploading a file again under its name keeps its id and makes what it was a version; a version
// comes back as the current one; someone who may not change the file gets no «Same» file.
func TestDriveVersions(t *testing.T) {
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
	co, _ := s.Open(ctx, anna, "company")
	coIvan, _ := s.Open(ctx, ivan, "company")
	var id string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, created_at)
		VALUES ('k1', 'план.docx', 1, $1, 'company', now() - interval '1 day') RETURNING id`, anna).Scan(&id))

	if got, _ := s.Same(ctx, anna, co, "", "план.docx"); got != id {
		t.Fatalf("Same = %q", got)
	}
	if got, _ := s.Same(ctx, anna, co, "", "другой.docx"); got != "" {
		t.Errorf("Same of another name = %q", got)
	}
	if !coIvan.CanManage {
		if got, _ := s.Same(ctx, ivan, coIvan, "", "план.docx"); got != "" {
			t.Errorf("Same for someone who may not change it = %q", got)
		}
		if err := s.AddVersion(ctx, ivan, coIvan, id, NewVersion{Key: "kx", Size: 9, ContentType: "x"}); !errors.Is(err, ErrForbidden) {
			t.Errorf("ivan's version: %v", err)
		}
	}
	must(s.AddVersion(ctx, anna, co, id, NewVersion{Key: "k2", Size: 2, ContentType: "application/x-2"}))
	must(s.AddVersion(ctx, anna, co, id, NewVersion{Key: "k3", Size: 3, ContentType: "application/x-3"}))
	vs, err := s.Versions(ctx, id)
	must(err)
	if len(vs) != 3 || vs[0].ID != "" || vs[0].No != 3 || vs[0].Size != 3 || vs[1].Size != 2 || vs[2].Size != 1 || vs[2].No != 1 || vs[0].Author != "Анна" {
		t.Fatalf("versions = %+v", vs)
	}
	e, err := s.VersionObject(ctx, id, vs[2].ID)
	must(err)
	if e.Key != "k1" || e.Size != 1 {
		t.Errorf("version object = %+v", e)
	}
	if _, err := s.VersionObject(ctx, id, vs[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the current one as a version: %v", err)
	}
	must(s.RestoreVersion(ctx, anna, co, id, vs[2].ID))
	var key string
	var size int64
	must(pool.QueryRow(ctx, `SELECT object_key, size FROM files WHERE id = $1`, id).Scan(&key, &size))
	if key != "k1" || size != 1 {
		t.Errorf("restored = %s %d", key, size)
	}
	if vs, _ := s.Versions(ctx, id); len(vs) != 4 || vs[1].Size != 3 {
		t.Errorf("after restore = %+v", vs)
	}
	if err := s.RestoreVersion(ctx, anna, co, id, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("restore of nothing: %v", err)
	}
	// a file in the trash has no versions to add
	must(s.DeleteFile(ctx, anna, co, id))
	if err := s.AddVersion(ctx, anna, co, id, NewVersion{Key: "k4"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("version of a trashed file: %v", err)
	}
}

// TestDriveSearch: a search goes through all the folders of the drive but not into the trash, finds folders by name;
// «Последние» are the user's own files lately, on the drives they may read.
func TestDriveSearch(t *testing.T) {
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
	co, _ := s.Open(ctx, anna, "company")
	my, _ := s.Open(ctx, anna, "my")
	addFile := func(by, drive, owner, folder, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id)
			VALUES (gen_random_uuid()::text, $1, 3, $2, $3, $4, $5) RETURNING id`, name, by, drive, nullable(owner), nullable(folder)).Scan(&id))
		return id
	}
	docs, err := s.CreateFolder(ctx, anna, co, "", "Документы")
	must(err)
	plans, err := s.CreateFolder(ctx, anna, co, docs, "Планы 100%")
	must(err)
	old, err := s.CreateFolder(ctx, anna, co, "", "Старое")
	must(err)
	deep := addFile(anna, "company", "", plans, "план_2026.docx")
	top := addFile(ivan, "company", "", "", "ПЛАН.xlsx")
	addFile(anna, "company", "", old, "план старый.docx")
	mine := addFile(anna, "user", anna, "", "мой план.txt")
	addFile(ivan, "user", ivan, "", "план Ивана.txt")
	must(s.DeleteFolder(ctx, anna, co, old))

	folders, files, err := s.Search(ctx, co, "план")
	must(err)
	got := map[string]string{}
	for _, f := range files {
		got[f.ID] = f.Where
	}
	if len(files) != 2 || got[deep] != "Планы 100%" || got[top] != "" {
		t.Errorf("found files = %+v", files)
	}
	if len(folders) != 1 || folders[0].ID != plans || folders[0].Where != "Документы" {
		t.Errorf("found folders = %+v", folders)
	}
	if _, files, _ := s.Search(ctx, co, "_"); len(files) != 1 || files[0].ID != deep {
		t.Errorf("«_» is a letter, not any: %+v", files)
	}
	if folders, _, _ := s.Search(ctx, co, "100%"); len(folders) != 1 {
		t.Errorf("«%%» is a letter: %+v", folders)
	}
	if folders, files, _ := s.Search(ctx, co, ""); len(folders) != 0 || len(files) != 2 {
		t.Errorf("an empty search = %d folders, %d files", len(folders), len(files))
	}
	if _, files, _ := s.Search(ctx, my, "план"); len(files) != 1 || files[0].ID != mine {
		t.Errorf("my drive = %+v", files)
	}

	recent, err := s.Recent(ctx, anna, 30, 10)
	must(err)
	if len(recent) != 2 || recent[0].Drive == "" {
		t.Errorf("anna's recent = %+v", recent)
	}
	for _, r := range recent {
		if r.ID != deep && r.ID != mine {
			t.Errorf("recent has %s", r.Name)
		}
	}
	if recent, _ := s.Recent(ctx, anna, 30, 1); len(recent) != 1 {
		t.Errorf("recent is not limited: %d", len(recent))
	}
	// a version anna uploaded of ivan's file (as one who manages the drive) is hers lately
	_, err = pool.Exec(ctx, `UPDATE files SET changed_by = $2, created_at = now() + interval '1 second' WHERE id = $1`, top, anna)
	must(err)
	if recent, _ := s.Recent(ctx, anna, 30, 10); len(recent) != 3 || recent[0].ID != top {
		t.Errorf("after a version = %+v", recent)
	}
}

// TestDriveShares: a folder of «Мой диск» shared with an employee or a department is a drive of its own for them —
// only what it holds, with the access given; it is in their «Общие со мной» until closed or put into the trash.
func TestDriveShares(t *testing.T) {
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
	var anna, ivan, olga, top, sales string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'i@t') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('olga', 'Ольга', 'o@t') RETURNING id`).Scan(&olga))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&top))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Продажи') RETURNING id`, top).Scan(&sales))
	var south string
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Юг') RETURNING id`, sales).Scan(&south))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, olga, south)
	must(err)
	s := NewStore(pool, tasks.NewStore(pool))
	my, _ := s.Open(ctx, anna, "my")
	ivanMy, _ := s.Open(ctx, ivan, "my")
	docs, err := s.CreateFolder(ctx, anna, my, "", "Документы")
	must(err)
	scans, err := s.CreateFolder(ctx, anna, my, docs, "Сканы")
	must(err)
	private, err := s.CreateFolder(ctx, anna, my, "", "Личное")
	must(err)
	addFile := func(by, folder, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id)
			VALUES (gen_random_uuid()::text, $1, 3, $2, 'user', $3, $4) RETURNING id`, name, by, anna, nullable(folder)).Scan(&id))
		return id
	}
	contract := addFile(anna, scans, "договор.pdf")
	secret := addFile(anna, private, "секрет.txt")
	topFile := addFile(anna, "", "верх.txt")

	if _, err := s.Open(ctx, ivan, "sh:"+docs); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not shared yet: %v", err)
	}
	if err := s.SetShare(ctx, ivan, ivanMy, docs, "", "user", ivan, "read"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ivan shares anna's folder: %v", err)
	}
	if err := s.SetShare(ctx, anna, my, docs, "", "user", ivan, "write"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a bad access: %v", err)
	}
	if err := s.SetShare(ctx, anna, my, docs, "", "user", "00000000-0000-0000-0000-000000000000", "read"); !errors.Is(err, ErrNotFound) {
		t.Errorf("nobody: %v", err)
	}
	must(s.SetShare(ctx, anna, my, docs, "", "user", ivan, "read"))
	notes := func(who string) (n int, url string) { // «открыл(а) вам папку на Диске»
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT count(*), coalesce(max(url), '') FROM notifications WHERE user_id = $1 AND kind = 'disk_share'
			AND actor_id = $2 AND body = 'Документы'`, who, anna).Scan(&n, &url))
		return n, url
	}
	if n, url := notes(ivan); n != 1 || url != "/disk/sh/"+docs {
		t.Errorf("ivan's notifications = %d %s", n, url)
	}
	sh, err := s.Open(ctx, ivan, "sh:"+docs)
	must(err)
	if sh.CanWrite || sh.CanManage || sh.Name != "Документы" || sh.Owner != "Анна" || sh.Path() != "/disk/sh/"+docs || sh.At("") != docs || sh.At(scans) != scans {
		t.Fatalf("read drive = %+v", sh)
	}
	folders, files, trail, err := s.List(ctx, sh, "")
	must(err)
	if len(folders) != 1 || folders[0].ID != scans || len(files) != 0 || len(trail) != 0 {
		t.Errorf("the shared top = %v %v %v", folders, files, trail)
	}
	if _, files, trail, _ := s.List(ctx, sh, scans); len(files) != 1 || len(trail) != 1 || trail[0].ID != scans {
		t.Errorf("inside = %v %v", files, trail)
	}
	if _, _, _, err := s.List(ctx, sh, private); !errors.Is(err, ErrNotFound) {
		t.Errorf("a folder outside: %v", err)
	}
	if err := s.CheckFolder(ctx, sh, ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("reading only, ivan uploads: %v", err)
	}
	if !s.SharedFileTo(ctx, ivan, contract) || s.SharedFileTo(ctx, ivan, secret) || s.SharedFileTo(ctx, ivan, topFile) || s.SharedFileTo(ctx, olga, contract) {
		t.Error("SharedFileTo is wrong")
	}
	if _, files, _ := s.Search(ctx, sh, ""); len(files) != 1 || files[0].ID != contract {
		t.Errorf("search within the shared folder = %v", files)
	}
	if _, err := s.Trash(ctx, sh); !errors.Is(err, ErrForbidden) {
		t.Errorf("the owner's trash: %v", err)
	}

	// edit: ivan makes folders at the shared top, deletes his own, not anna's, nor the shared folder itself
	must(s.SetShare(ctx, anna, my, docs, "", "user", ivan, "edit"))
	sh, _ = s.Open(ctx, ivan, "sh:"+docs)
	if !sh.CanWrite || sh.CanManage {
		t.Fatalf("edit drive = %+v", sh)
	}
	his, err := s.CreateFolder(ctx, ivan, sh, "", "Иван")
	must(err)
	var parent string
	must(pool.QueryRow(ctx, `SELECT parent_id::text FROM drive_folders WHERE id = $1`, his).Scan(&parent))
	if parent != docs {
		t.Errorf("ivan's folder is in %s", parent)
	}
	if err := s.DeleteFile(ctx, ivan, sh, contract); !errors.Is(err, ErrForbidden) {
		t.Errorf("ivan deletes anna's file: %v", err)
	}
	if err := s.RenameFolder(ctx, ivan, sh, docs, "Моё"); !errors.Is(err, ErrForbidden) {
		t.Errorf("ivan renames the shared folder: %v", err)
	}
	if err := s.Move(ctx, ivan, sh, Selection{Folders: []string{his}}, ivanMy, ""); err != nil {
		t.Errorf("ivan takes his folder to his drive: %v", err)
	}
	if err := s.Move(ctx, ivan, ivanMy, Selection{Folders: []string{his}}, sh, ""); err != nil {
		t.Errorf("and back: %v", err)
	}
	must(s.DeleteFolder(ctx, ivan, sh, his))

	// a department: olga of «Юг» gets what is shared with «Продажи»
	must(s.SetShare(ctx, anna, my, docs, "", "department", sales, "full"))
	if n, _ := notes(olga); n != 1 { // in a department below
		t.Errorf("olga's notifications = %d", n)
	}
	must(s.SetShare(ctx, anna, my, docs, "", "department", sales, "full"))
	if n, _ := notes(olga); n != 1 {
		t.Errorf("sharing again notifies again: %d", n)
	}
	if sh, err := s.Open(ctx, olga, "sh:"+docs); err != nil || !sh.CanManage {
		t.Errorf("olga through her department: %+v %v", sh, err)
	}
	if sh, _ := s.Open(ctx, ivan, "sh:"+docs); sh.Access != "edit" {
		t.Errorf("ivan has %s", sh.Access)
	}
	shares, err := s.Shares(ctx, []string{docs})
	must(err)
	if len(shares[docs]) != 2 || shares[docs][1].Whom != "Продажи" || shares[docs][1].Kind != "department" {
		t.Errorf("shares = %+v", shares)
	}
	if list, _ := s.SharedWithMe(ctx, olga); len(list) != 1 || list[0].ID != docs || list[0].Owner != "Анна" || list[0].Access != "full" {
		t.Errorf("olga's shared = %+v", list)
	}
	if list, _ := s.SharedWithMe(ctx, anna); len(list) != 0 {
		t.Errorf("anna's own folder is shared with her: %+v", list)
	}
	if err := s.DeleteShare(ctx, ivan, ivanMy, shares[docs][0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("ivan closes anna's share: %v", err)
	}
	must(s.DeleteShare(ctx, anna, my, shares[docs][0].ID))
	if _, err := s.Open(ctx, ivan, "sh:"+docs); !errors.Is(err, ErrNotFound) {
		t.Errorf("closed: %v", err)
	}
	// in the trash, it is shared with nobody
	must(s.DeleteFolder(ctx, anna, my, docs))
	if list, _ := s.SharedWithMe(ctx, olga); len(list) != 0 {
		t.Errorf("a trashed folder is shared: %+v", list)
	}
	if _, err := s.Open(ctx, olga, "sh:"+docs); !errors.Is(err, ErrNotFound) {
		t.Errorf("a trashed folder opens: %v", err)
	}
	if s.SharedFileTo(ctx, olga, contract) {
		t.Error("a file of a trashed folder is shared")
	}
}

// TestDriveLimits: what a drive holds counts its files, their former versions and its trash (a shared folder's —
// its owner's); a limit refuses more; the administrator's list has every drive, the fullest first.
func TestDriveLimits(t *testing.T) {
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
	co, _ := s.Open(ctx, anna, "company")
	add := func(drive string, owner *string, size int64, deleted bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, deleted_at)
			VALUES (gen_random_uuid()::text, 'f', $1, $2, $3, $4, CASE WHEN $5 THEN now() END) RETURNING id`, size, anna, drive, owner, deleted).Scan(&id))
		return id
	}
	f := add("user", &anna, 100, false)
	add("user", &anna, 50, true) // in the trash
	add("company", nil, 1000, false)
	add("user", &ivan, 10, false)
	must(s.AddVersion(ctx, anna, my, f, NewVersion{Key: "v2", Size: 30, ContentType: "text/plain"})) // 100 kept as a version
	if u, err := s.Usage(ctx, my); err != nil || u.Used != 180 || u.Limit != 0 || u.Percent() != 0 {
		t.Fatalf("my usage = %+v %v", u, err)
	}
	must(s.Fits(ctx, my, 1<<40)) // no limit
	must(s.SaveLimits(ctx, Limits{User: 200, Company: 5000}))
	if l, _ := s.Limits(ctx); l != (Limits{User: 200, Company: 5000}) {
		t.Errorf("limits = %+v", l)
	}
	if u, _ := s.Usage(ctx, my); u.Limit != 200 || u.Percent() != 90 {
		t.Errorf("my usage = %+v", u)
	}
	must(s.Fits(ctx, my, 20))
	if err := s.Fits(ctx, my, 21); !errors.Is(err, ErrFull) {
		t.Errorf("over the limit: %v", err)
	}
	must(s.Fits(ctx, co, 4000))
	folder, err := s.CreateFolder(ctx, anna, my, "", "Общая")
	must(err)
	must(s.SetShare(ctx, anna, my, folder, "", "user", ivan, "edit"))
	sh, err := s.Open(ctx, ivan, "sh:"+folder)
	must(err)
	if err := s.Fits(ctx, sh, 21); !errors.Is(err, ErrFull) { // Ivan uploads into Anna's drive
		t.Errorf("a shared folder is its owner's drive: %v", err)
	}
	if err := s.SaveLimits(ctx, Limits{User: -1}); err == nil {
		t.Error("a negative limit is kept")
	}
	list, err := s.Usages(ctx)
	must(err)
	if len(list) != 3 || list[0].Kind != "company" || list[0].Used != 1000 || list[1].Name != "Анна" || list[1].Used != 180 || list[2].Used != 10 {
		t.Errorf("usages = %+v", list)
	}
}

// TestDriveEnsurePath: an uploaded folder's path is made folder by folder; folders already there (in any case) take
// the files in, a parallel upload making the same folder does not fail; «..» and readers are refused.
func TestDriveEnsurePath(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool, tasks.NewStore(pool))
	my, err := s.Open(ctx, anna, "my")
	must(err)
	photos, err := s.CreateFolder(ctx, anna, my, "", "Фото")
	must(err)

	deep, err := s.EnsurePath(ctx, anna, my, "", "фото/2024/Лето")
	must(err)
	_, _, path, err := s.List(ctx, my, deep)
	must(err)
	if len(path) != 3 || path[0].ID != photos || path[1].Name != "2024" || path[2].Name != "Лето" {
		t.Fatalf("path = %+v", path)
	}
	if again, err := s.EnsurePath(ctx, anna, my, "", `Фото\2024/./Лето/`); err != nil || again != deep {
		t.Errorf("the same path again = %s %v, want %s", again, err, deep)
	}
	if top, err := s.EnsurePath(ctx, anna, my, photos, ""); err != nil || top != photos {
		t.Errorf("an empty path = %s %v", top, err)
	}
	// parallel uploads of one folder
	errs := make(chan error, 8)
	ids := make(chan string, 8)
	for range 8 {
		go func() { id, err := s.EnsurePath(ctx, anna, my, "", "Сканы/Договоры"); ids <- id; errs <- err }()
	}
	first := ""
	for range 8 {
		must(<-errs)
		if id := <-ids; first == "" {
			first = id
		} else if id != first {
			t.Errorf("parallel uploads made two folders: %s, %s", first, id)
		}
	}
	for _, bad := range []string{"a/../b", strings.Repeat("x/", 40)} {
		if _, err := s.EnsurePath(ctx, anna, my, "", bad); err == nil {
			t.Errorf("EnsurePath(%q) is taken", bad)
		}
	}
	my.CanWrite = false
	if _, err := s.EnsurePath(ctx, anna, my, "", "Новая"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a reader makes folders: %v", err)
	}
}
