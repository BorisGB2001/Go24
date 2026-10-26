package tasks

import (
	"context"
	"testing"
)

// TestParentFollows: a subtask taken into work starts its waiting parent and the parent's parent (history shows
// it); a started or deferred parent stays as it is; when the last subtask is done, the parent's creator is asked to
// complete it (once, not the creator who closes it himself), and the parent is not closed by itself.
func TestParentFollows(t *testing.T) {
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
	boss, dev := newUser("boss"), newUser("dev")
	pk, err := s.CreateProject(ctx, boss, "pf", "Родители", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, boss, pk)
	must(err)
	add := func(title, parent string) (string, string) {
		key, err := s.CreateTask(ctx, boss, proj, NewTask{Title: title, ResponsibleID: dev, ParentKey: parent})
		must(err)
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		return key, id
	}
	status := func(id string) Status {
		var st Status
		must(pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id = $1`, id).Scan(&st))
		return st
	}
	asked := func(id string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'subtasks_done' AND task_id = $1 AND user_id = $2`, id, boss).Scan(&n))
		return n
	}
	epicKey, epic := add("Эпик", "")
	partKey, part := add("Часть", epicKey)
	_, sub1 := add("Шаг 1", partKey)
	_, sub2 := add("Шаг 2", partKey)

	must(s.ApplyAction(ctx, dev, sub1, ActionStart))
	if status(part) != StatusInProgress || status(epic) != StatusInProgress {
		t.Fatalf("parents after a subtask started: %s, %s", status(part), status(epic))
	}
	var by string
	must(pool.QueryRow(ctx, `SELECT actor_id::text FROM task_history WHERE task_id = $1 AND field = 'status' AND new_value = 'in_progress'`, epic).Scan(&by))
	if by != dev {
		t.Fatalf("the epic's start is by %s", by)
	}

	must(s.ApplyAction(ctx, dev, sub1, ActionComplete))
	if asked(part) != 0 {
		t.Fatal("asked with a subtask still open")
	}
	must(s.ApplyAction(ctx, dev, sub2, ActionStart))
	must(s.ApplyAction(ctx, dev, sub2, ActionComplete))
	if asked(part) != 1 || status(part) != StatusInProgress {
		t.Fatalf("the last subtask done: asked %d, part %s", asked(part), status(part))
	}

	// a deferred parent is not started by its subtask
	must(s.ApplyAction(ctx, boss, epic, ActionDefer))
	_, sub3 := add("Шаг 3", epicKey)
	must(s.ApplyAction(ctx, dev, sub3, ActionStart))
	if status(epic) != StatusDeferred {
		t.Fatalf("a deferred parent became %s", status(epic))
	}
	// the creator closing the last subtask himself is not asked
	_, own := add("Свой", partKey)
	must(s.ApplyAction(ctx, boss, own, ActionComplete))
	if asked(part) != 1 {
		t.Fatalf("the creator asked about his own closing: %d", asked(part))
	}
}
