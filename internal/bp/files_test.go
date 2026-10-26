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
	"go24/internal/tasks"
)

// TestFileProcesses: a template of the documents of «Общий диск» starts by itself on a document uploaded there (as its
// uploader), another on a new version — not on a board's save, a file of «Мой диск» or one brought back from the trash;
// {title} and {card} are the file's, «ответственный» — who uploaded it; by hand it starts for one who sees the file,
// not for one its folder is closed to; CRM steps are not for files.
func TestFileProcesses(t *testing.T) {
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
	autoLag = 0
	var ann, bob, boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'ann', 'ann@t') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'bob', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	ds := drive.NewStore(pool, tasks.NewStore(pool))
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))
	s.Files = ds
	file := func(name, kind string, owner *string, folder *string) string {
		t.Helper()
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by, drive, drive_owner, folder_id)
			VALUES ('k/' || gen_random_uuid(), $1, 1, 'text/plain', $2, $3, $4, $5) RETURNING id`, name, ann, kind, owner, folder).Scan(&id))
		return id
	}
	runs := func(tpl, id string) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM bp_instances WHERE template_id::text = $1 AND entity_id::text = $2`, tpl, id).Scan(&n))
		return n
	}

	// CRM steps are not for files
	for _, st := range []Step{{Kind: "update", Sets: []Assign{{Field: "title", Value: "x"}}}, {Kind: "comment", Text: "x"},
		{Kind: "mail", To: "client", Subject: "x", Text: "x"}, {Kind: "stage", Stage: "x"}} {
		if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Entity: EntityFile, Steps: []Step{st}}); err == nil {
			t.Fatalf("a %s step saved in a template of files", st.Kind)
		}
	}
	onAdd, err := s.SaveTemplate(ctx, boss, Template{Name: "Утверждение документа", Entity: EntityFile, Active: true, AutoCreate: true,
		Steps: []Step{{Kind: "approve", To: "responsible", Text: "Утвердите {title}: {card}"}}})
	must(err)
	onChange, err := s.SaveTemplate(ctx, boss, Template{Name: "Новая версия", Entity: EntityFile, Active: true, AutoChange: true,
		Steps: []Step{{Kind: "notify", To: "starter", Text: "{title}"}}})
	must(err)

	doc := file("Договор.docx", "company", nil, nil)
	mine := file("Моё.docx", "user", &ann, nil)
	board := file("Доска.board", "company", nil, nil)
	must(errors.Join(s.AutoStartFiles(ctx)...))
	if runs(onAdd, doc) != 1 || runs(onAdd, mine) != 0 || runs(onAdd, board) != 1 || runs(onChange, doc) != 0 {
		t.Fatalf("autostart on upload: doc %d, mine %d, board %d, change %d", runs(onAdd, doc), runs(onAdd, mine), runs(onAdd, board), runs(onChange, doc))
	}
	var who, text, starter string
	must(pool.QueryRow(ctx, `SELECT a.user_id::text, a.text, i.started_by::text FROM bp_approvals a JOIN bp_instances i ON i.id = a.instance_id
		WHERE i.entity_id::text = $1`, doc).Scan(&who, &text, &starter))
	if who != ann || starter != ann || text != "Утвердите Договор.docx: /files/"+doc+"/about" {
		t.Fatalf("approval: %s %s %q", who, starter, text)
	}

	// a new version starts the other one; a board's save, a version of a file in the trash do not
	_, err = pool.Exec(ctx, `UPDATE files SET object_key = 'k/v2/' || id, changed_by = $2 WHERE id::text IN ($1, $3)`, doc, bob, board)
	must(err)
	must(errors.Join(s.AutoStartFiles(ctx)...))
	if runs(onChange, doc) != 1 || runs(onChange, board) != 0 {
		t.Fatalf("autostart on change: doc %d, board %d", runs(onChange, doc), runs(onChange, board))
	}
	must(pool.QueryRow(ctx, `SELECT started_by::text FROM bp_instances WHERE template_id::text = $1`, onChange).Scan(&starter))
	if starter != bob {
		t.Fatalf("started by %s, want who changed it", starter)
	}
	_, err = pool.Exec(ctx, `UPDATE files SET deleted_at = now() WHERE id::text = $1`, doc)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE files SET deleted_at = NULL WHERE id::text = $1`, doc)
	must(err)
	must(errors.Join(s.AutoStartFiles(ctx)...))
	if runs(onAdd, doc) != 1 || runs(onChange, doc) != 1 {
		t.Fatalf("brought back from the trash: add %d, change %d", runs(onAdd, doc), runs(onChange, doc))
	}
	// moved to «Общий диск» from «Мой диск» — added there
	_, err = pool.Exec(ctx, `UPDATE files SET drive = 'company', drive_owner = NULL WHERE id::text = $1`, mine)
	must(err)
	must(errors.Join(s.AutoStartFiles(ctx)...))
	if runs(onAdd, mine) != 1 {
		t.Fatal("a file moved to «Общий диск» did not start the template")
	}

	// by hand: one who sees the document starts, one its folder is closed to does not; the card's processes
	var folder string
	must(pool.QueryRow(ctx, `INSERT INTO drive_folders (drive, name, created_by) VALUES ('company', 'Закрытая', $1) RETURNING id`, boss).Scan(&folder))
	secret := file("Секрет.docx", "company", nil, &folder)
	actx := tasks.WithAdmin(ctx)
	cd, err := ds.Open(actx, boss, "company")
	must(err)
	must(ds.SetRight(actx, boss, cd, folder, "user", ann, "full"))
	manual, err := s.SaveTemplate(ctx, boss, Template{Name: "Вручную", Entity: EntityFile, Active: true,
		Steps: []Step{{Kind: "notify", To: "starter", Text: "{title}"}}})
	must(err)
	if _, err := s.Start(ctx, bob, manual, secret, nil); !errors.Is(err, drive.ErrNotFound) && !errors.Is(err, drive.ErrForbidden) {
		t.Fatalf("started on a closed folder's file: %v", err)
	}
	if _, err := s.Start(ctx, ann, manual, secret, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CardInstances(ctx, bob, EntityFile, secret); err == nil {
		t.Fatal("bob sees the processes of a file closed to him")
	}
	list, err := s.CardInstances(ctx, ann, EntityFile, secret)
	must(err)
	if len(list) != 1 || list[0].Name != "Вручную" {
		t.Fatalf("card's processes: %+v", list)
	}
	if _, err := s.Start(ctx, ann, manual, mine, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, ann, manual, file("Ещё.docx", "user", &ann, nil), nil); err == nil {
		t.Fatal("started on a file of «Мой диск»")
	}
}
