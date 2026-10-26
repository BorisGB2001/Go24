package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"go24/internal/i18n"
)

// TestTaskParams: the task parameters. «Не завершать задачу без результата» — completing (or sending to
// control) waits for a result; «Сроки определяются сроками подзадач» — the deadline and plan dates follow the
// subtasks (new, moved, in the trash), up the tree, and are not set by hand; «Автоматически завершать задачу при
// завершении подзадач» — the last subtask done completes the parent (to control when its creator is someone else),
// and the grandparent with it; templates and edits keep the parameters.
func TestTaskParams(t *testing.T) {
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
	pk, err := s.CreateProject(ctx, boss, "pa", "Параметры", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, boss, pk)
	must(err)
	add := func(in NewTask) (string, string) {
		t.Helper()
		if in.ResponsibleID == "" {
			in.ResponsibleID = dev
		}
		key, err := s.CreateTask(ctx, boss, proj, in)
		must(err)
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		return key, id
	}
	card := func(id string) TaskCard {
		t.Helper()
		c, err := s.GetCard(ctx, boss, id)
		must(err)
		return c
	}
	errKey := func(err error) string {
		var e *i18n.Error
		if errors.As(err, &e) {
			return e.Key
		}
		return ""
	}
	at := func(day int) *time.Time {
		v := time.Date(2026, 11, day, 18, 0, 0, 0, time.UTC)
		return &v
	}

	// the result is required
	_, res := add(NewTask{Title: "Отчёт", Params: Params{ResultRequired: true}})
	if err := s.ApplyAction(ctx, dev, res, ActionComplete); errKey(err) != "err.result_required" {
		t.Fatalf("completed without a result: %v", err)
	}
	c, err := s.PostComment(ctx, dev, res, "Готово: отчёт во вложении")
	must(err)
	must(s.SetResult(ctx, dev, res, c.ID, true))
	must(s.ApplyAction(ctx, dev, res, ActionComplete))
	if st := card(res).Status; st != StatusCompleted {
		t.Fatalf("with a result the task is %s", st)
	}

	// the dates follow the subtasks
	epicKey, epic := add(NewTask{Title: "Эпик", Params: Params{DatesFromSubtasks: true}})
	partKey, part := add(NewTask{Title: "Часть", ParentKey: epicKey, Deadline: at(5), Params: Params{DatesFromSubtasks: true}})
	if dl := card(epic).Deadline; dl == nil || !dl.Equal(*at(5)) {
		t.Fatalf("the epic's deadline after a subtask: %v", dl)
	}
	_, a := add(NewTask{Title: "Шаг А", ParentKey: partKey, Deadline: at(10)})
	_, b := add(NewTask{Title: "Шаг Б", ParentKey: partKey, Deadline: at(20)})
	if dl := card(epic).Deadline; dl == nil || !dl.Equal(*at(20)) {
		t.Fatalf("the epic's deadline two levels up: %v", dl)
	}
	must(s.SetDeadline(ctx, boss, a, at(25)))
	if dl := card(part).Deadline; dl == nil || !dl.Equal(*at(25)) {
		t.Fatalf("the part's deadline after a subtask moved: %v", dl)
	}
	sp, ep := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	cb := card(b)
	must(s.UpdateTask(ctx, boss, b, TaskUpdate{Version: cb.Version, Title: cb.Title, Deadline: cb.Deadline, StartPlan: &sp, EndPlan: &ep}))
	if pc := card(part); dateText(pc.StartPlan) != "2026-11-02" || dateText(pc.EndPlan) != "2026-11-12" {
		t.Fatalf("the part's plan: %s — %s", dateText(pc.StartPlan), dateText(pc.EndPlan))
	}
	must(s.DeleteTask(ctx, boss, a))
	if dl := card(epic).Deadline; dl == nil || !dl.Equal(*at(20)) {
		t.Fatalf("the epic's deadline after a subtask went to the trash: %v", dl)
	}
	if err := s.SetDeadline(ctx, boss, part, at(28)); errKey(err) != "err.deadline_from_subtasks" {
		t.Fatalf("the derived deadline was set by hand: %v", err)
	}
	pc := card(part)
	if pc.CanChangeDeadline() {
		t.Fatal("the card offers the derived deadline")
	}
	must(s.UpdateTask(ctx, boss, part, TaskUpdate{Version: pc.Version, Title: "Часть 1", Deadline: at(28), Params: pc.Params}))
	if pc := card(part); pc.Title != "Часть 1" || !pc.Deadline.Equal(*at(20)) {
		t.Fatalf("an edit of the part: %q, %v", pc.Title, pc.Deadline)
	}
	// without the box the dates are the task's own again; ticking it takes the subtasks' dates at once
	pc = card(part)
	must(s.UpdateTask(ctx, boss, part, TaskUpdate{Version: pc.Version, Title: pc.Title, Deadline: at(28)}))
	if pc := card(part); !pc.Deadline.Equal(*at(28)) || pc.DatesFromSubtasks {
		t.Fatalf("the part without the box: %v, %v", pc.Deadline, pc.DatesFromSubtasks)
	}
	pc = card(part)
	must(s.UpdateTask(ctx, boss, part, TaskUpdate{Version: pc.Version, Title: pc.Title, Deadline: pc.Deadline, Params: Params{DatesFromSubtasks: true}}))
	if pc := card(part); !pc.Deadline.Equal(*at(20)) {
		t.Fatalf("the part with the box ticked again: %v", pc.Deadline)
	}

	// auto-complete: the last subtask done completes the parent and the grandparent
	topKey, top := add(NewTask{Title: "Верх", Params: Params{AutoComplete: true}})
	midKey, mid := add(NewTask{Title: "Середина", ParentKey: topKey, Params: Params{AutoComplete: true}})
	_, x := add(NewTask{Title: "Икс", ParentKey: midKey})
	_, y := add(NewTask{Title: "Игрек", ParentKey: midKey})
	must(s.ApplyAction(ctx, dev, x, ActionComplete))
	if st := card(mid).Status; st == StatusCompleted {
		t.Fatal("completed with a subtask open")
	}
	must(s.ApplyAction(ctx, dev, y, ActionComplete))
	if st1, st2 := card(mid).Status, card(top).Status; st1 != StatusCompleted || st2 != StatusCompleted {
		t.Fatalf("after the last subtask: %s, %s", st1, st2)
	}
	// to control, when the parent's creator is someone else; with a result required and none, the creator is asked
	ctlKey, ctl := add(NewTask{Title: "Контроль", RequiresControl: true, Params: Params{AutoComplete: true}})
	_, z := add(NewTask{Title: "Зет", ParentKey: ctlKey})
	must(s.ApplyAction(ctx, dev, z, ActionComplete))
	if st := card(ctl).Status; st != StatusAwaitingControl {
		t.Fatalf("a parent under control became %s", st)
	}
	needKey, need := add(NewTask{Title: "С результатом", Params: Params{AutoComplete: true, ResultRequired: true}})
	_, w := add(NewTask{Title: "Вэ", ParentKey: needKey})
	must(s.ApplyAction(ctx, dev, w, ActionComplete))
	var asked int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'subtasks_done' AND task_id = $1`, need).Scan(&asked))
	if st := card(need).Status; st == StatusCompleted || asked != 1 {
		t.Fatalf("a parent without its result: %s, asked %d", st, asked)
	}

	// a template keeps the parameters
	id, err := s.SaveTemplate(ctx, boss, Template{ProjectKey: pk, Title: "Шаблон", ResponsibleID: dev,
		Params: Params{DatesFromSubtasks: true, ResultRequired: true}})
	must(err)
	tpl, err := s.Template(ctx, boss, id)
	must(err)
	if !tpl.DatesFromSubtasks || tpl.AutoComplete || !tpl.ResultRequired || tpl.NewTask(time.Now()).Params != tpl.Params {
		t.Fatalf("the template's parameters: %+v", tpl.Params)
	}
}
