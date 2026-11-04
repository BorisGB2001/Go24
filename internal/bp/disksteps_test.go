package bp

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestDiskSteps: a process makes a folder on «Общий диск», copies a document into it, reads the document (a message
// goes to its uploader), moves it to the starter's «Мой диск» and puts the folder into the trash; one who may not
// change a document of another fails to delete it, and a document of the process's own is its file.
func TestDiskSteps(t *testing.T) {
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
	boss, ann, bob := newUser("boss"), newUser("Анна"), newUser("Боб")
	ts := tasks.NewStore(pool)
	ds := drive.NewStore(pool, ts)
	s := NewStore(pool, ts, crm.NewStore(pool), company.NewStore(pool))
	s.Files, s.Drive = ds, ds
	copied := 0
	s.CopyBytes = func(context.Context, *string, string, string, int64, string) error { copied++; return nil }
	type message struct{ from, to, text string }
	var messages []message
	s.Chat = func(_ context.Context, from, to, text string) error {
		messages = append(messages, message{from, to, text})
		return nil
	}
	var doc string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by, drive)
		VALUES ('k/doc', 'Договор.docx', 2048, 'application/msword', $1, 'company') RETURNING id`, ann).Scan(&doc))

	var ve *i18n.Error
	bad := []Step{
		{Kind: "diskfolder", Folder: "a"},                     // no drive
		{Kind: "diskfolder", Drive: "company", Folder: " / "}, // no path
		{Kind: "diskcopy"},                                    // no drive
		{Kind: "diskread", Drive: "company"},                  // a drive of a read
		{Kind: "diskdelete", Move: true},                      // a move of a deletion
		{Kind: "diskread", Folder: "a"},                       // a folder of a read
		{Kind: "diskcopy", Drive: "../x"},                     // no such drive
	}
	for i, st := range bad {
		if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Steps: []Step{st}}); !errors.As(err, &ve) {
			t.Fatalf("bad step %d saved: %v", i, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Архив договора", Active: true,
		Fields: []Field{{Key: "doc", Label: "Документ", Kind: "string"}},
		Steps: []Step{
			{Kind: "diskfolder", Drive: "company", Folder: "Договоры/{starter}"},
			{Kind: "diskcopy", Value: "/files/{doc}/about", Drive: "company", Folder: "Договоры/{starter}"},
			{Kind: "diskread", Value: "{doc}"},
			{Kind: "chat", From: boss, To: FieldPrefix + "disk_by", Text: "{disk_name}: {disk_size} · {disk_by}"},
			{Kind: "diskcopy", Value: "{disk_link}", Drive: "my", Move: true},
			{Kind: "diskread", Value: "{created_folder}"},
			{Kind: "diskdelete"},
		}})
	must(err)
	tp, err := s.Template(ctx, tpl)
	must(err)
	if f, ok := (Instance{Fields: AllFields(tp.Fields, tp.Steps)}).Field("disk_by"); !ok || f.Kind != "user" || UserInfoLabel(f.Key) != "bp.diskinfo.by" {
		t.Fatalf("the drive's fields: %+v", tp.Steps)
	}
	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"doc": doc})
	must(err)
	x, err := s.instance(ctx, id)
	must(err)
	if x.Status != "done" {
		lines, _ := s.Log(ctx, id)
		t.Fatalf("status %s: %+v", x.Status, lines)
	}
	if len(messages) != 1 || messages[0].to != ann || messages[0].text != "Договор.docx: 2048 · Анна" {
		t.Fatalf("messages: %+v", messages)
	}
	folder := x.Values["created_folder_id"]
	if x.Values["created_folder"] != "/disk/company?folder="+folder || x.Values["disk_type"] != "folder" || x.Values["disk_name"] != "Анна" ||
		x.Values["disk_folder"] != "Договоры" || x.Values["disk_size"] != "" {
		t.Fatalf("values: %+v", x.Values)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE folder_id = $1 AND name = 'Договор.docx'`, folder).Scan(&n))
	var where string
	must(pool.QueryRow(ctx, `SELECT drive || ':' || coalesce(drive_owner::text, '') FROM files WHERE id = $1`, doc).Scan(&where))
	var trashed bool
	must(pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM drive_folders WHERE id = $1`, folder).Scan(&trashed))
	if n != 1 || copied != 1 || where != "user:"+ann || !trashed {
		t.Fatalf("copies %d (%d bytes copied), the document on %s, the folder trashed %v", n, copied, where, trashed)
	}

	// Bob may not delete Anna's document of «Общий диск»; a process of the file deletes its own file, of its uploader
	var other string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by, drive)
		VALUES ('k/other', 'Счёт.pdf', 1, 'application/pdf', $1, 'company') RETURNING id`, ann).Scan(&other))
	del, err := s.SaveTemplate(ctx, boss, Template{Name: "Удаление", Entity: EntityFile, Active: true,
		Steps: []Step{{Kind: "diskdelete", From: "responsible"}}})
	must(err)
	byBob, err := s.SaveTemplate(ctx, boss, Template{Name: "Удаление Бобом", Entity: EntityFile, Active: true,
		Steps: []Step{{Kind: "diskdelete"}}})
	must(err)
	id, err = s.Start(ctx, bob, byBob, other, nil)
	must(err)
	if x, _ = s.instance(ctx, id); x.Status != "failed" {
		t.Fatalf("Bob deleted the document: %s", x.Status)
	}
	id, err = s.Start(ctx, bob, del, other, nil)
	must(err)
	must(pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM files WHERE id = $1`, other).Scan(&trashed))
	if x, _ = s.instance(ctx, id); x.Status != "done" || !trashed {
		t.Fatalf("the file's own process: %s, trashed %v", x.Status, trashed)
	}
}
