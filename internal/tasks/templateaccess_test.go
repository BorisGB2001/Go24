package tasks

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestTemplateAccess: «Права доступа» of a task template — «Чтение» to a person sees the template and makes a task by
// it but cannot change, pause or delete it; «Полный доступ» to a department reaches its sub-department's people, who
// change it and add subtask templates (the author's); nobody else sees it; the rights go when they are taken away.
func TestTemplateAccess(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	boss, reader, editor, stranger := user("boss"), user("reader"), user("editor"), user("stranger")
	var company, sales, east string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&company))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Продажи', $1) RETURNING id`, company).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Восток', $1) RETURNING id`, sales).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, editor, east)
	must(err)

	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	p, err := s.ProjectByKey(ctx, boss, "GO")
	must(err)
	for _, u := range []string{reader, editor, stranger} {
		must(s.SetMember(ctx, p, u, "member"))
	}
	base, err := s.SaveTemplate(ctx, boss, Template{ProjectKey: "GO", Title: "Отчёт", Repeat: Repeat{On: true, Period: "daily", Every: 1, At: "09:00"}})
	must(err)
	sub, err := s.SaveTemplate(ctx, boss, Template{ProjectKey: "GO", Title: "Цифры", ParentID: base})
	must(err)

	if list, _ := s.Templates(ctx, reader); len(list) != 0 {
		t.Fatalf("not shared yet: %+v", list)
	}
	if err := s.SetTemplateAccess(ctx, reader, base, []string{reader}, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a stranger shared the template: %v", err)
	}
	if err := s.SetTemplateAccess(ctx, boss, sub, []string{reader}, nil); err == nil {
		t.Fatal("rights of a subtask template")
	}
	must(s.SetTemplateAccess(ctx, boss, base, []string{reader, boss, "dept:" + sales}, []string{"dept:" + sales}))
	read, full, err := s.TemplateAccess(ctx, boss, base)
	must(err)
	if len(read) != 1 || read[0].ID != reader || len(full) != 1 || full[0].ID != "dept:"+sales || full[0].Name != "Продажи" {
		t.Fatalf("access: read %+v full %+v", read, full)
	}

	// «Чтение»
	list, err := s.Templates(ctx, reader)
	must(err)
	if len(list) != 1 || list[0].Shared != "read" || list[0].CanEdit() || list[0].CreatorName != "boss" {
		t.Fatalf("reader's list = %+v", list)
	}
	tpl, err := s.Template(ctx, reader, base)
	must(err)
	if len(tpl.Subtasks) != 1 {
		t.Fatalf("subtasks = %+v", tpl.Subtasks)
	}
	if st, err := s.Template(ctx, reader, sub); err != nil || st.Shared != "read" {
		t.Fatalf("subtask template for the reader: %+v %v", st, err)
	}
	key, err := s.CreateFromTemplate(ctx, reader, tpl, time.Now())
	must(err)
	id, err := s.TaskIDByKey(ctx, key)
	must(err)
	var creator string
	must(pool.QueryRow(ctx, `SELECT creator_id::text FROM tasks WHERE id = $1`, id).Scan(&creator))
	if creator != reader {
		t.Fatalf("the task is set by %s", creator)
	}
	tpl.Title = "Взлом"
	if _, err := s.SaveTemplate(ctx, reader, tpl); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the reader changed it: %v", err)
	}
	if _, err := s.SaveTemplate(ctx, reader, Template{ProjectKey: "GO", Title: "x", ParentID: base}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the reader added a subtask template: %v", err)
	}
	if err := s.PauseTemplate(ctx, reader, base, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the reader paused it: %v", err)
	}
	if err := s.DeleteTemplate(ctx, reader, sub); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the reader deleted a subtask template: %v", err)
	}

	// «Полный доступ» by a department above the editor's
	tpl, err = s.Template(ctx, editor, base)
	must(err)
	if tpl.Shared != "full" || !tpl.CanEdit() {
		t.Fatalf("editor: %+v", tpl)
	}
	tpl.Title = "Отчёт за день"
	_, err = s.SaveTemplate(ctx, editor, tpl)
	must(err)
	more, err := s.SaveTemplate(ctx, editor, Template{ProjectKey: "GO", Title: "Выводы", ParentID: base})
	must(err)
	var author string
	must(pool.QueryRow(ctx, `SELECT creator_id::text FROM task_templates WHERE id = $1`, more).Scan(&author))
	if author != boss {
		t.Fatalf("a subtask template by the editor belongs to %s", author)
	}
	if tpl, _ = s.Template(ctx, boss, base); tpl.Title != "Отчёт за день" || tpl.CreatorID != boss || len(tpl.Subtasks) != 2 {
		t.Fatalf("after the editor: %+v", tpl)
	}
	must(s.PauseTemplate(ctx, editor, base, false))
	must(s.SetTemplateAccess(ctx, editor, base, []string{reader}, []string{editor}))

	// nobody else; rights taken away
	if _, err := s.Template(ctx, stranger, base); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a stranger: %v", err)
	}
	if list, _ := s.Templates(ctx, stranger); len(list) != 0 {
		t.Fatalf("stranger's list = %+v", list)
	}
	must(s.SetTemplateAccess(ctx, boss, base, nil, nil))
	if _, err := s.Template(ctx, reader, base); !errors.Is(err, ErrForbidden) {
		t.Fatalf("after taking away: %v", err)
	}
	must(s.DeleteTemplate(ctx, boss, base))
}
