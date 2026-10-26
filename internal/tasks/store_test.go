package tasks

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestStoreScenario runs a full portal-style task lifecycle against a real PostgreSQL.
// It needs GO24_TEST_DATABASE_URL pointing to a throwaway database: the public schema is recreated.
func TestStoreScenario(t *testing.T) {
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

	newUser := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	creator, resp, auditor, stranger := newUser("creator"), newUser("resp"), newUser("auditor"), newUser("stranger")
	s := NewStore(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	wantErr := func(err, target error) {
		t.Helper()
		if !errors.Is(err, target) {
			t.Fatalf("err = %v, want %v", err, target)
		}
	}
	var ve *i18n.Error

	// projects
	if _, err := s.CreateProject(ctx, creator, "1x", "Bad", ""); !errors.As(err, &ve) {
		t.Fatalf("bad key must fail validation, got %v", err)
	}
	key, err := s.CreateProject(ctx, creator, "go", "Тест", "")
	must(err)
	if key != "GO" {
		t.Fatalf("key = %s", key)
	}
	if _, err := s.CreateProject(ctx, creator, "GO", "Дубль", ""); !errors.As(err, &ve) {
		t.Fatalf("duplicate key must fail validation, got %v", err)
	}
	p, err := s.ProjectByKey(ctx, creator, "go")
	must(err)
	_, err = s.ProjectByKey(ctx, stranger, "GO")
	wantErr(err, ErrForbidden)
	must(s.SetMember(ctx, p, resp, "member"))

	// tasks
	k1, err := s.CreateTask(ctx, creator, p, NewTask{Title: "Родитель", ResponsibleID: resp, RequiresControl: true, Auditors: []string{auditor}})
	must(err)
	k2, err := s.CreateTask(ctx, creator, p, NewTask{Title: "Подзадача", ResponsibleID: resp, ParentKey: k1})
	must(err)
	if k1 != "GO-1" || k2 != "GO-2" {
		t.Fatalf("keys = %s, %s", k1, k2)
	}
	id1, err := s.TaskIDByKey(ctx, k1)
	must(err)

	_, err = s.GetCard(ctx, stranger, id1)
	wantErr(err, ErrForbidden)
	card, err := s.GetCard(ctx, auditor, id1)
	must(err)
	// the auditor of GO-1 is not a participant of the subtask GO-2 and not a project member: GO-2 stays hidden
	if len(card.Actions) != 0 || len(card.Subtasks) != 0 || len(card.Auditors) != 1 {
		t.Fatalf("auditor card: actions=%v subtasks=%d auditors=%d", card.Actions, len(card.Subtasks), len(card.Auditors))
	}

	if cc, _ := s.GetCard(ctx, creator, id1); len(cc.Subtasks) != 1 {
		t.Fatalf("creator must see the subtask, got %d", len(cc.Subtasks))
	}

	// workflow with result control
	must(s.ApplyAction(ctx, resp, id1, ActionStart))
	must(s.ApplyAction(ctx, resp, id1, ActionComplete))
	if c, _ := s.GetCard(ctx, creator, id1); c.Status != StatusAwaitingControl {
		t.Fatalf("status after complete = %s", c.Status)
	}
	if err := s.ApplyAction(ctx, resp, id1, ActionApprove); !errors.As(err, &ve) {
		t.Fatalf("responsible must not approve, got %v", err)
	}
	must(s.ApplyAction(ctx, creator, id1, ActionApprove))
	card, err = s.GetCard(ctx, creator, id1)
	must(err)
	if card.Status != StatusCompleted || card.ClosedAt == nil {
		t.Fatalf("after approve: status=%s closed=%v", card.Status, card.ClosedAt)
	}

	// kanban moves: the responsible may start GO-2 (pending → in_progress), the auditor may not move anything
	list, err := s.ListTasks(ctx, Filter{ProjectID: p.ID})
	must(err)
	moves, err := s.Moves(ctx, resp, list)
	must(err)
	id2, _ := s.TaskIDByKey(ctx, k2)
	if moves[id2][StatusInProgress] != ActionStart {
		t.Fatalf("resp moves for GO-2 = %v", moves[id2])
	}
	if am, _ := s.Moves(ctx, auditor, list); len(am[id1]) != 0 {
		t.Fatalf("auditor moves = %v", am[id1])
	}

	// my tasks
	open, err := s.ListTasks(ctx, Filter{UserID: resp, Role: "responsible", OnlyOpen: true})
	must(err)
	if len(open) != 1 || open[0].Key() != "GO-2" {
		t.Fatalf("resp open tasks = %v", open)
	}
	given, err := s.ListTasks(ctx, Filter{UserID: creator, Role: "creator"})
	must(err)
	if len(given) != 2 {
		t.Fatalf("creator «поручил» = %d", len(given))
	}

	// optimistic locking
	upd := TaskUpdate{Version: card.Version - 1, Title: "Новое", ResponsibleID: resp}
	wantErr(s.UpdateTask(ctx, creator, id1, upd), ErrConflict)
	upd.Version = card.Version
	must(s.UpdateTask(ctx, creator, id1, upd))
	wantErr(s.UpdateTask(ctx, resp, id1, upd), ErrForbidden)

	// checklist and comments
	must(s.AddChecklistItem(ctx, resp, id1, "Пункт"))
	wantErr(s.AddChecklistItem(ctx, auditor, id1, "Нельзя"), ErrForbidden)
	card, _ = s.GetCard(ctx, resp, id1)
	must(s.ChangeChecklistItem(ctx, resp, id1, card.Checklist[0].ID, false))
	must(s.AddComment(ctx, auditor, id1, "Вижу"))
	card, err = s.GetCard(ctx, creator, id1)
	must(err)
	if card.ChecklistDone() != 1 || len(card.Comments) != 1 || card.Title != "Новое" {
		t.Fatalf("card: done=%d comments=%d title=%s", card.ChecklistDone(), len(card.Comments), card.Title)
	}
	// time tracking (id2 = GO-2 from the kanban block above)
	must(s.StartTimer(ctx, resp, id1))
	if _, err := pool.Exec(ctx, `UPDATE task_timers SET started_at = now() - interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	must(s.StartTimer(ctx, resp, id2)) // stops the timer on GO-1 and logs ~10 minutes
	if tm, _ := s.RunningTimer(ctx, resp); tm == nil || tm.TaskKey != "GO-2" {
		t.Fatalf("running timer = %+v", tm)
	}
	must(s.StopTimer(ctx, resp))
	if tm, _ := s.RunningTimer(ctx, resp); tm != nil {
		t.Fatalf("timer still running: %+v", tm)
	}
	wantErr(s.StartTimer(ctx, auditor, id1), ErrForbidden)
	must(s.AddTime(ctx, resp, id1, 5400, time.Now(), "работа"))
	entries, total, err := s.TaskTime(ctx, id1)
	must(err)
	if total < 5400+600 || len(entries) != 2 {
		t.Fatalf("time: total=%d entries=%d", total, len(entries))
	}
	var manual string
	for _, e := range entries {
		if e.Source == "manual" {
			manual = e.ID
		}
	}
	wantErr(s.DeleteTime(ctx, auditor, id1, manual), ErrForbidden)
	// the author corrects an entry; someone else may not
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	wantErr(s.UpdateTime(ctx, auditor, id1, manual, 600, day, "x"), ErrForbidden)
	must(s.UpdateTime(ctx, resp, id1, manual, 2700, day, " исправлено "))
	entries, _, err = s.TaskTime(ctx, id1)
	must(err)
	for _, e := range entries {
		if e.ID == manual && (e.Seconds != 2700 || e.Comment != "исправлено" || e.SpentOn.Format("2006-01-02") != "2026-10-01") {
			t.Fatalf("updated entry = %+v", e)
		}
	}
	var timeLog int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_history WHERE task_id = $1 AND field = 'time'`, id1).Scan(&timeLog))
	if timeLog < 2 { // logged and corrected (and the timer)
		t.Fatalf("time changes in the history = %d", timeLog)
	}
	est := 240
	card, _ = s.GetCard(ctx, creator, id1)
	must(s.UpdateTask(ctx, creator, id1, TaskUpdate{Version: card.Version, Title: card.Title, ResponsibleID: card.ResponsibleID, EstimateMinutes: &est}))
	card, _ = s.GetCard(ctx, creator, id1)
	if card.EstimateMinutes == nil || *card.EstimateMinutes != 240 || card.TimeTotal < 2700 {
		t.Fatalf("estimate=%v total=%d", card.EstimateMinutes, card.TimeTotal)
	}
	must(s.DeleteTime(ctx, resp, id1, manual))

	// «Мой план»: built-in columns, moving, personal stages
	cols, err := s.Plan(ctx, resp)
	must(err)
	if len(cols) != 4 || cols[1].Code != "today" || len(cols[0].Tasks) != 1 { // only GO-2 is open for resp
		t.Fatalf("plan columns = %+v", cols)
	}
	must(s.MovePlan(ctx, resp, id2, cols[1].ID))
	cols, _ = s.Plan(ctx, resp)
	if len(cols[0].Tasks) != 0 || len(cols[1].Tasks) != 1 {
		t.Fatalf("after move: unplanned=%d today=%d", len(cols[0].Tasks), len(cols[1].Tasks))
	}
	creatorCols, _ := s.Plan(ctx, creator)
	wantErr(s.MovePlan(ctx, resp, id2, creatorCols[2].ID), ErrForbidden) // someone else's column
	must(s.RenameStage(ctx, resp, cols[1].ID, "Срочно"))
	must(s.DeleteStage(ctx, resp, cols[1].ID))
	if cols, _ = s.Plan(ctx, resp); len(cols) != 3 || len(cols[0].Tasks) != 1 {
		t.Fatalf("after delete: %+v", cols)
	}

	// Gantt: links with cycle protection, plan dates
	must(s.AddLink(ctx, creator, id2, k1)) // GO-1 must finish before GO-2
	if err := s.AddLink(ctx, creator, id1, k2); !errors.As(err, &ve) || ve.Key != "err.link_cycle" {
		t.Fatalf("cycle not refused: %v", err)
	}
	if err := s.AddLink(ctx, creator, id2, k2); !errors.As(err, &ve) || ve.Key != "err.link_self" {
		t.Fatalf("self link not refused: %v", err)
	}
	wantErr(s.AddLink(ctx, auditor, id2, k1), ErrForbidden)
	pred, succ, err := s.TaskLinks(ctx, id2)
	must(err)
	if len(pred) != 1 || pred[0].FromKey != "GO-1" || len(succ) != 0 {
		t.Fatalf("links: pred=%v succ=%v", pred, succ)
	}
	start, end := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	c2, _ := s.GetCard(ctx, creator, id2)
	bad := TaskUpdate{Version: c2.Version, Title: c2.Title, ResponsibleID: c2.ResponsibleID, StartPlan: &end, EndPlan: &start}
	if err := s.UpdateTask(ctx, creator, id2, bad); !errors.As(err, &ve) || ve.Key != "err.plan_dates" {
		t.Fatalf("reversed plan dates accepted: %v", err)
	}
	must(s.UpdateTask(ctx, creator, id2, TaskUpdate{Version: c2.Version, Title: c2.Title, ResponsibleID: c2.ResponsibleID, StartPlan: &start, EndPlan: &end}))
	g, err := s.Gantt(ctx, creator, p.ID, true)
	must(err)
	if len(g.Tasks) != 2 || len(g.Links) != 1 {
		t.Fatalf("gantt: tasks=%d links=%d", len(g.Tasks), len(g.Links))
	}
	for _, gt := range g.Tasks {
		if gt.ID == id2 && (!gt.Planned || !gt.Start.Equal(start) || !gt.End.Equal(end)) {
			t.Fatalf("GO-2 bar = %v..%v planned=%v", gt.Start, gt.End, gt.Planned)
		}
	}
	must(s.RemoveLink(ctx, creator, id2, pred[0].ID))

	// Visibility "participants" tasks without a group): project role alone shows nothing
	_, err = s.CreateProject(ctx, creator, "PR", "Личные", "")
	must(err)
	pr, _ := s.ProjectByKey(ctx, creator, "PR")
	must(s.SetMember(ctx, pr, stranger, "member"))
	must(s.SetVisibility(ctx, pr, "participants"))
	pr, _ = s.ProjectByKey(ctx, creator, "PR")
	kp, err := s.CreateTask(ctx, creator, pr, NewTask{Title: "Секретное поручение", ResponsibleID: resp})
	must(err)
	idp, _ := s.TaskIDByKey(ctx, kp)
	must(s.SetVisibility(ctx, pr, "participants"))
	// the stranger is a project member but not a participant
	if l, _ := s.ListTasks(ctx, Filter{ProjectID: pr.ID, Viewer: stranger}); len(l) != 0 {
		t.Fatalf("member sees a participants-only task: %v", l)
	}
	if l, _ := s.Search(ctx, stranger, "Секретное"); len(l) != 0 {
		t.Fatalf("search leaks a participants-only task: %v", l)
	}
	_, err = s.GetCard(ctx, stranger, idp)
	wantErr(err, ErrForbidden)
	if ps, _ := s.ProjectByKey(ctx, stranger, "PR"); ps.OpenTasks != 0 {
		t.Fatalf("open-task count leaks: %d", ps.OpenTasks)
	}
	// the creator and the responsible do see it; the admin in admin mode sees it too
	if l, _ := s.ListTasks(ctx, Filter{ProjectID: pr.ID, Viewer: resp}); len(l) != 1 {
		t.Fatalf("responsible must see the task, got %d", len(l))
	}
	if l, _ := s.ListTasks(ctx, Filter{ProjectID: pr.ID, Viewer: stranger}); len(l) != 0 {
		t.Fatal("unreachable")
	}
	adminCtx := WithAdmin(ctx)
	if _, err := s.GetCard(adminCtx, stranger, idp); err != nil {
		t.Fatalf("admin mode must see everything: %v", err)
	}
	if l, _ := s.ListTasks(adminCtx, Filter{Viewer: stranger}); len(l) < 3 {
		t.Fatalf("admin «all tasks» = %d", len(l))
	}
	// with "members" the project member sees it again
	must(s.SetVisibility(ctx, pr, "members"))
	if l, _ := s.ListTasks(ctx, Filter{ProjectID: pr.ID, Viewer: stranger}); len(l) != 1 {
		t.Fatalf("members visibility: %d", len(l))
	}

	// sidebar counters and the lists they link to are the same selections
	cnt, err := s.Counters(ctx, resp)
	must(err)
	openL, _ := s.ListTasks(ctx, Filter{UserID: resp, Doing: true, OnlyOpen: true})
	overdueL, _ := s.ListTasks(ctx, Filter{UserID: resp, Doing: true, Overdue: true})
	controlL, _ := s.ListTasks(ctx, Filter{CreatorID: resp, Status: StatusAwaitingControl})
	if cnt.Open != len(openL) || cnt.Overdue != len(overdueL) || cnt.ToControl != len(controlL) {
		t.Fatalf("counters %+v vs lists open=%d overdue=%d control=%d", cnt, len(openL), len(overdueL), len(controlL))
	}

	if len(card.History) < 5 { // created, 3 status changes, title
		t.Fatalf("history entries = %d", len(card.History))
	}
}
