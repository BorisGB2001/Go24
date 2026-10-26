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
	"go24/internal/worktime"
)

// TestStages checks the project kanban against a real PostgreSQL (see TestStoreScenario).
func TestStages(t *testing.T) {
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
	creator, resp, stranger := newUser("creator"), newUser("resp"), newUser("stranger")
	s := NewStore(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	wantKey := func(err error, key string) {
		t.Helper()
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != key {
			t.Fatalf("err = %v, want %s", err, key)
		}
	}

	_, err = s.CreateProject(ctx, creator, "GO", "go24", "")
	must(err)
	p, err := s.ProjectByKey(ctx, creator, "GO")
	must(err)
	must(s.SetMember(ctx, p, resp, "member"))
	pm, err := s.ProjectByKey(ctx, resp, "GO")
	must(err)

	// default stages, finish last
	st, err := s.Stages(ctx, p.ID)
	must(err)
	if len(st) != 3 || st[0].Code != "new" || st[1].Code != "work" || !st[2].Finish() {
		t.Fatalf("default stages = %+v", st)
	}
	newSt, work, finish := st[0].ID, st[1].ID, st[2].ID

	stageOfTask := func(key string) (string, Status) {
		t.Helper()
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		tk, err := s.TaskByID(ctx, id)
		must(err)
		return tk.StageID, tk.Status
	}

	// create into a stage; never straight into the finish stage
	key, err := s.CreateTask(ctx, creator, p, NewTask{Title: "В работе", ResponsibleID: resp, StageID: work})
	must(err)
	if sid, _ := stageOfTask(key); sid != work {
		t.Fatalf("stage = %s, want work", sid)
	}
	_, err = s.CreateTask(ctx, creator, p, NewTask{Title: "x", StageID: finish})
	wantKey(err, "err.stage_finish_new")
	id, _ := s.TaskIDByKey(ctx, key)

	// a task without a stage (e.g. imported) sits in the first column; the board counts it
	plain, err := s.CreateTask(ctx, creator, p, NewTask{Title: "Без стадии", ResponsibleID: resp})
	must(err)
	cols, err := s.Board(ctx, resp, pm)
	must(err)
	if cols[0].Total != 1 || cols[0].Tasks[0].Key() != plain || cols[1].Total != 1 {
		t.Fatalf("board = %+v", cols)
	}

	// strangers cannot move
	if err := s.MoveToStage(ctx, stranger, id, newSt); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger move: %v", err)
	}
	// a stage of another project is not found
	_, err = s.CreateProject(ctx, creator, "OT", "other", "")
	must(err)
	op, _ := s.ProjectByKey(ctx, creator, "OT")
	ost, err := s.Stages(ctx, op.ID)
	must(err)
	if err := s.MoveToStage(ctx, resp, id, ost[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign stage: %v", err)
	}

	// drop into finish completes; dragging out renews
	must(s.MoveToStage(ctx, resp, id, finish))
	if sid, stt := stageOfTask(key); sid != finish || stt != StatusCompleted {
		t.Fatalf("after finish: %s %s", sid, stt)
	}
	must(s.MoveToStage(ctx, resp, id, newSt))
	if sid, stt := stageOfTask(key); sid != newSt || stt != StatusPending {
		t.Fatalf("after renew: %s %s", sid, stt)
	}

	// with result review: the responsible sends to control; only the creator can drag it back out
	ctl, err := s.CreateTask(ctx, creator, p, NewTask{Title: "С контролем", ResponsibleID: resp, RequiresControl: true})
	must(err)
	cid, _ := s.TaskIDByKey(ctx, ctl)
	must(s.MoveToStage(ctx, resp, cid, finish))
	if sid, stt := stageOfTask(ctl); sid != finish || stt != StatusAwaitingControl {
		t.Fatalf("control: %s %s", sid, stt)
	}
	wantKey(s.MoveToStage(ctx, resp, cid, work), "err.action_not_allowed")
	if sid, _ := stageOfTask(ctl); sid != finish {
		t.Fatal("refused move changed the stage")
	}
	must(s.MoveToStage(ctx, creator, cid, work))
	if sid, stt := stageOfTask(ctl); sid != work || stt != StatusPending {
		t.Fatalf("disapproved: %s %s", sid, stt)
	}

	// status changes on the card keep the kanban in step
	must(s.ApplyAction(ctx, creator, cid, ActionComplete)) // creator completes directly
	if sid, _ := stageOfTask(ctl); sid != finish {
		t.Fatalf("completed task stage = %s", sid)
	}
	must(s.ApplyAction(ctx, creator, cid, ActionRenew))
	if sid, _ := stageOfTask(ctl); sid != newSt {
		t.Fatalf("renewed task stage = %s", sid)
	}

	// history records the stage
	card, err := s.GetCard(ctx, creator, id)
	must(err)
	found := false
	for _, h := range card.History {
		found = found || (h.Field == "stage" && h.New == "stage.finish")
	}
	if !found {
		t.Fatalf("no stage history: %+v", card.History)
	}

	// managing stages: managers only; a new stage goes before finish; shifting; deleting rules
	if err := s.AddProjectStage(ctx, pm, "Ревью", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member adds stage: %v", err)
	}
	wantKey(s.AddProjectStage(ctx, p, "Ревью", "red"), "err.stage_color")
	must(s.AddProjectStage(ctx, p, "Ревью", "#ffab00"))
	st, _ = s.Stages(ctx, p.ID)
	if len(st) != 4 || st[2].Title != "Ревью" || st[2].Color != "FFAB00" || !st[3].Finish() {
		t.Fatalf("after add: %+v", st)
	}
	review := st[2].ID
	must(s.ShiftProjectStage(ctx, p, review, -1))
	must(s.ShiftProjectStage(ctx, p, review, -1))
	must(s.ShiftProjectStage(ctx, p, review, -1)) // already first: no change
	st, _ = s.Stages(ctx, p.ID)
	if st[0].ID != review || st[1].ID != newSt || !st[3].Finish() {
		t.Fatalf("after shift: %+v", st)
	}
	must(s.UpdateProjectStage(ctx, p, newSt, "Входящие", "47D1E2"))
	wantKey(s.DeleteProjectStage(ctx, p, finish), "err.stage_finish_fixed")
	wantKey(s.DeleteProjectStage(ctx, p, newSt), "err.stage_not_empty")
	must(s.DeleteProjectStage(ctx, p, review))
	must(s.DeleteProjectStage(ctx, op, ost[1].ID))
	wantKey(s.DeleteProjectStage(ctx, op, ost[0].ID), "err.stage_last")

	// card counters
	must(s.AddChecklistItem(ctx, resp, id, "a"))
	must(s.AddChecklistItem(ctx, resp, id, "b"))
	must(s.AddComment(ctx, resp, id, "ok"))
	info, err := s.CardInfo(ctx, []string{id})
	must(err)
	if c := info[id]; c.ChecklistTotal != 2 || c.ChecklistDone != 0 || c.Comments != 1 {
		t.Fatalf("card info = %+v", c)
	}

	// list columns: defaults, saving, unknown names refused
	lc, err := s.Columns(ctx, resp)
	must(err)
	if !lc["status"] || lc["closed"] {
		t.Fatalf("default columns = %v", lc)
	}
	must(s.SetColumns(ctx, resp, []string{"closed", "id", "closed"}))
	lc, _ = s.Columns(ctx, resp)
	if len(lc) != 2 || !lc["closed"] || !lc["id"] {
		t.Fatalf("saved columns = %v", lc)
	}
	wantKey(s.SetColumns(ctx, resp, []string{"password"}), "err.bad_form")
	must(s.SetColumns(ctx, resp, nil)) // everything off except the title
	if lc, _ = s.Columns(ctx, resp); len(lc) != 0 {
		t.Fatalf("empty choice = %v", lc)
	}

	// deadline rules: the responsible needs permission; working time is respected
	locked, err := s.CreateTask(ctx, creator, p, NewTask{Title: "Срок", ResponsibleID: resp})
	must(err)
	lid, _ := s.TaskIDByKey(ctx, locked)
	sat := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local)
	wantKey(s.SetDeadline(ctx, resp, lid, &sat), "err.deadline_locked")
	must(s.SetDeadline(ctx, creator, lid, &sat)) // the creator may; no work-time rule on this task
	if tk, _ := s.TaskByID(ctx, lid); !tk.Deadline.Equal(sat) {
		t.Fatalf("deadline = %v", tk.Deadline)
	}
	free, err := s.CreateTask(ctx, creator, p, NewTask{Title: "Гибкий срок", ResponsibleID: resp,
		AllowChangeDeadline: true, MatchWorkTime: true, Deadline: &sat})
	must(err)
	fid, _ := s.TaskIDByKey(ctx, free)
	mon := time.Date(2026, 10, 19, 18, 0, 0, 0, time.Local)
	if tk, _ := s.TaskByID(ctx, fid); !tk.Deadline.Equal(mon) {
		t.Fatalf("created on Saturday: deadline = %v, want Monday 18:00", tk.Deadline)
	}
	wed := time.Date(2026, 10, 21, 20, 0, 0, 0, time.Local)
	must(s.SetDeadline(ctx, resp, fid, &wed))
	if tk, _ := s.TaskByID(ctx, fid); !tk.Deadline.Equal(time.Date(2026, 10, 21, 18, 0, 0, 0, time.Local)) {
		t.Fatalf("after hours: deadline = %v", tk.Deadline)
	}
	must(s.SetDeadline(ctx, resp, fid, nil))
	if tk, _ := s.TaskByID(ctx, fid); tk.Deadline != nil {
		t.Fatal("deadline not cleared")
	}
	wantKey(s.SetSchedule(ctx, creator, worktime.Schedule{Start: "18:00", End: "09:00"}), "err.worktime")
	must(s.SetSchedule(ctx, creator, worktime.Schedule{Start: "10:00", End: "19:00", Weekend: []int{0}}))
	must(s.SetDeadline(ctx, resp, fid, &sat)) // Saturday is a working day now
	if tk, _ := s.TaskByID(ctx, fid); !tk.Deadline.Equal(sat) {
		t.Fatalf("custom schedule: deadline = %v", tk.Deadline)
	}
}
