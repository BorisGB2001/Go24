package bp

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestFileFields: «Файл» fields, as the parameters of that type — the starter's own fresh upload only,
// the file is the process's (its people and the readers of its post see it, others do not; the approval shows it),
// {field} is the name and the address, «Изменить переменную» copies it, a request's answer attaches one, «Задача» and
// «Письмо» carry the files.
func TestFileFields(t *testing.T) {
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
	upload := func(by, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by) VALUES ($1, $2, 3, 'text/plain', $3)
			RETURNING id`, "k/"+by+"/"+name, name, by).Scan(&id))
		return id
	}
	ann, ivan, olga := newUser("ann"), newUser("ivan"), newUser("olga")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, ann, "OPS", "Работы", "")
	must(err)
	s := NewStore(pool, ts, crm.NewStore(pool), company.NewStore(pool))
	s.CopyBytes = func(_ context.Context, _ *string, from, to string, _ int64, _ string) error { return nil }
	s.OpenFile = func(_ context.Context, _ *string, key string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("of " + key)), nil
	}
	var mailed []ProcessFile
	s.MailFiles = func(_ context.Context, _, to, _, _ string, files []ProcessFile) error {
		mailed = files
		return nil
	}

	var ve *i18n.Error
	for name, f := range map[string]Field{
		"a default":  {Key: "doc", Label: "Файл", Kind: "file", Default: "00000000-0000-0000-0000-000000000000"},
		"a constant": {Key: "doc", Label: "Файл", Kind: "file", Scope: "const"},
	} {
		if _, err := s.SaveTemplate(ctx, ann, Template{Name: "Плохой", Fields: []Field{f}, Steps: []Step{{Kind: "log", Text: "x"}}}); !errors.As(err, &ve) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := s.SaveGlobals(ctx, ann, []Field{{Key: "doc", Label: "Файл", Kind: "file", Scope: "var"}}); !errors.As(err, &ve) {
		t.Fatalf("a global file: %v", err)
	}

	tpl, err := s.SaveTemplate(ctx, ann, Template{Name: "Счёт", Active: true, Fields: []Field{
		{Key: "doc", Label: "Счёт", Kind: "file", Required: true},
		{Key: "more", Label: "Ещё", Kind: "file", Multiple: true},
		{Key: "copy", Label: "Копия", Kind: "file", Scope: "var"},
	}, Steps: []Step{
		{Kind: "approve", To: ivan, Text: "Оплатить {doc}?"},
		{Kind: "set", Field: "copy", Value: "{doc}"},
		{Kind: "log", Text: "Счёт: {doc}; копия: {copy}"},
		{Kind: "request", To: ivan, Text: "Платёжка", Fields: []Field{{Key: "paid", Label: "Платёжка", Kind: "file"}}},
		{Kind: "task", Text: "Учесть", To: olga, Project: "OPS", Attach: true},
		{Kind: "mail", From: "starter", Email: "buh@example.com", Subject: "Счёт", Text: "{doc}", Attach: true},
	}})
	must(err)

	doc, more1, more2 := upload(ann, "счёт.pdf"), upload(ann, "акт.pdf"), upload(ann, "договор.pdf")
	if _, err := s.Start(ctx, ann, tpl, "", map[string]string{"doc": upload(ivan, "чужой.pdf")}); !errors.As(err, &ve) || ve.Key != "err.bp_file" {
		t.Fatalf("someone's file: %v", err)
	}
	if _, err := s.Start(ctx, ann, tpl, "", map[string]string{"doc": "not a file"}); !errors.As(err, &ve) {
		t.Fatalf("not an id: %v", err)
	}
	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"doc": doc, "more": more1 + Multi + more2})
	must(err)
	if _, err := s.Start(ctx, ann, tpl, "", map[string]string{"doc": doc}); !errors.As(err, &ve) || ve.Key != "err.bp_file" {
		t.Fatalf("a file of another process: %v", err)
	}
	if !s.FileSeen(ctx, ann, more2) || !s.FileSeen(ctx, ivan, doc) || s.FileSeen(ctx, olga, doc) {
		t.Fatal("who sees the files")
	}
	list, err := s.MyApprovals(ctx, ivan)
	must(err)
	if len(list) != 1 || len(list[0].Files) != 3 || list[0].Files[0].Name != "акт.pdf" || !strings.Contains(list[0].Text, "счёт.pdf /files/"+doc) {
		t.Fatalf("the approval: %+v", list)
	}
	must(s.Decide(ctx, ivan, list[0].ID, "approve", ""))

	x, err := s.Instance(ctx, ann, id)
	must(err)
	if x.Values["copy"] != doc || x.Step != 3 {
		t.Fatalf("the copy: %v %d", x.Values, x.Step)
	}
	var logText string
	must(pool.QueryRow(ctx, `SELECT text FROM bp_log WHERE instance_id = $1 AND kind = 'report'`, id).Scan(&logText))
	if logText != "Счёт: счёт.pdf /files/"+doc+"; копия: счёт.pdf /files/"+doc {
		t.Fatalf("the text: %s", logText)
	}

	// the answer: someone's file is refused, the person's own is the process's
	list, err = s.MyApprovals(ctx, ivan)
	must(err)
	if err := s.Answer(ctx, ivan, list[0].ID, map[string]string{"paid": upload(olga, "её.pdf")}, ""); !errors.As(err, &ve) || ve.Key != "err.bp_file" {
		t.Fatalf("an answer with someone's file: %v", err)
	}
	paid := upload(ivan, "платёжка.pdf")
	must(s.Answer(ctx, ivan, list[0].ID, map[string]string{"paid": paid}, ""))
	if !s.FileSeen(ctx, ann, paid) {
		t.Fatal("the answer's file is the process's")
	}

	// «Задача» with the files: copies of the four files, the task's own
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files f JOIN tasks t ON t.id = f.task_id WHERE t.title = 'Учесть'
		AND f.name IN ('счёт.pdf', 'акт.pdf', 'договор.pdf', 'платёжка.pdf')`).Scan(&n))
	if n != 4 {
		t.Fatalf("the task's files: %d", n)
	}
	// «Письмо» with the files
	if len(mailed) != 4 || mailed[0].Name == "" || !strings.HasPrefix(string(mailed[0].Data), "of k/") {
		t.Fatalf("the letter's files: %+v", mailed)
	}
	if x, _ = s.Instance(ctx, ann, id); x.Status != "done" {
		t.Fatalf("the process: %s %d", x.Status, x.Step)
	}
}
