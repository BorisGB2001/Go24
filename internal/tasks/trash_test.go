package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestTrash: the creator deletes a task with its subtasks (others may not), it disappears from lists and keys,
// the trash shows it to the creator, and it comes back with its comments and subtasks; purge and the company setting
// delete for good; a subtask restored while its parent is in the trash comes back on its own.
func TestTrash(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	creator, resp, stranger := newUser("creator"), newUser("resp"), newUser("stranger")
	p, err := s.CreateProject(ctx, creator, "TR", "Корзина", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, creator, p)
	must(err)
	must(s.SetMember(ctx, proj, resp, "member"))
	create := func(by, title, parent string) string {
		key, err := s.CreateTask(ctx, by, proj, NewTask{Title: title, ResponsibleID: resp, ParentKey: parent})
		must(err)
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		return id
	}
	root := create(creator, "Отчёт", "")
	sub := create(creator, "Раздел", "TR-1")
	subsub := create(creator, "Таблица", "TR-2")
	must(s.AddComment(ctx, resp, root, "готово наполовину"))
	must(s.AddChecklistItem(ctx, creator, root, "проверить цифры"))

	if err := s.DeleteTask(ctx, resp, root); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the responsible deletes: %v", err)
	}
	if err := s.DeleteTask(ctx, stranger, root); err == nil {
		t.Fatal("a stranger deletes")
	}
	must(s.DeleteTask(ctx, creator, root))
	for _, id := range []string{root, sub, subsub} {
		if _, err := s.TaskByID(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted task still found: %v", err)
		}
	}
	if _, err := s.TaskIDByKey(ctx, "TR-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("key of a deleted task: %v", err)
	}
	list, err := s.ListTasks(ctx, Filter{UserID: resp, Role: "responsible"})
	must(err)
	if len(list) != 0 {
		t.Fatalf("deleted tasks in a list: %d", len(list))
	}
	trash, err := s.Trash(ctx, creator)
	must(err)
	if len(trash) != 1 || trash[0].ID != root || trash[0].Subtasks != 2 || trash[0].DeletedByName != "creator" || trash[0].PurgeAt == nil {
		t.Fatalf("trash: %+v", trash)
	}
	if trash, _ := s.Trash(ctx, resp); len(trash) != 0 {
		t.Fatalf("the responsible sees the trash: %+v", trash)
	}
	if trash, _ := s.Trash(WithAdmin(ctx), stranger); len(trash) != 1 {
		t.Fatalf("admin trash: %+v", trash)
	}
	if err := s.RestoreTask(ctx, resp, root); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the responsible restores: %v", err)
	}
	if err := s.RestoreTask(ctx, creator, sub); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a subtask restored apart from its parent: %v", err)
	}

	must(s.RestoreTask(ctx, creator, root))
	card, err := s.GetCard(ctx, creator, root)
	must(err)
	if len(card.Comments) != 1 || len(card.Checklist) != 1 || len(card.Subtasks) != 1 || card.SubTotal != 1 {
		t.Fatalf("restored card: comments %d, checklist %d, subtasks %d", len(card.Comments), len(card.Checklist), len(card.Subtasks))
	}
	var fields []string
	rows, err := pool.Query(ctx, `SELECT field FROM task_history WHERE task_id = $1 AND field IN ('deleted', 'restored') ORDER BY id`, root)
	must(err)
	for rows.Next() {
		var f string
		must(rows.Scan(&f))
		fields = append(fields, f)
	}
	if len(fields) != 2 || fields[0] != "deleted" || fields[1] != "restored" {
		t.Fatalf("history: %v", fields)
	}

	// a subtask deleted on its own, then its parent: the subtask comes back without the parent
	must(s.DeleteTask(ctx, creator, sub))
	must(s.DeleteTask(ctx, creator, root))
	must(s.RestoreTask(ctx, creator, sub))
	if tk, err := s.TaskByID(ctx, sub); err != nil || tk.ParentKey != "" {
		t.Fatalf("subtask back with a parent in the trash: %+v %v", tk, err)
	}
	if tk, err := s.TaskByID(ctx, subsub); err != nil || tk.ParentKey == "" {
		t.Fatalf("its own subtask: %+v %v", tk, err)
	}

	// for good: by hand, and by the setting
	must(s.PurgeTask(ctx, creator, root))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM tasks_all WHERE id = $1`, root).Scan(&n))
	if n != 0 {
		t.Fatal("purged task still kept")
	}
	if days, _ := s.TrashDays(ctx); days != DefaultTrashDays {
		t.Fatalf("default days %d", days)
	}
	must(s.DeleteTask(ctx, creator, sub))
	if n, err := s.PurgeExpired(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("purged too early: %d %v", n, err)
	}
	if n, err := s.PurgeExpired(ctx, time.Now().AddDate(0, 0, DefaultTrashDays+1)); err != nil || n != 1 {
		t.Fatalf("expired: %d %v", n, err)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM tasks_all`).Scan(&n))
	if n != 0 {
		t.Fatalf("tasks left: %d", n)
	}
	must(s.SetTrashDays(ctx, creator, 0))
	if days, _ := s.TrashDays(ctx); days != 0 {
		t.Fatalf("days %d", days)
	}
	if err := s.SetTrashDays(ctx, creator, -1); err == nil {
		t.Fatal("negative days saved")
	}
}
