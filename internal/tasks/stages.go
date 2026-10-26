package tasks

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Stage is a kanban column of a project (the portal group stage).
type Stage struct {
	ID    string
	Code  string // new | work | finish for built-in stages, "" for custom ones
	Title string // custom title; built-in stages without one take it from the locale (stage.<code>)
	Color string // RRGGBB
}

func (s Stage) Finish() bool { return s.Code == "finish" }

// StageColumn is a stage with its cards. Total counts all tasks in the stage; Tasks may be shorter.
type StageColumn struct {
	Stage
	Tasks []Task
	Total int
}

// FinishLimit keeps the finish column short, like the portal kanban that loads it lazily.
const FinishLimit = 30

var colorRe = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)

// StageColors is the palette offered for new stages (the portal defaults go first).
var StageColors = []string{"00C4FB", "47D1E2", "75D900", "FFAB00", "FF5752", "F968B6", "8E52EC", "1EAE43", "A8ADB4"}

// ensureProjectStages creates the default stages (Новые, Выполняются, Сделаны) on first use.
func ensureProjectStages(ctx context.Context, q querier, projectID string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO project_stages (project_id, code, color, sort)
		SELECT $1, c.code, c.color, c.sort FROM (VALUES ('new', '00C4FB', 10), ('work', '47D1E2', 20), ('finish', '75D900', 30)) AS c(code, color, sort)
		WHERE NOT EXISTS (SELECT 1 FROM project_stages WHERE project_id = $1)`, projectID)
	return err
}

// Stages returns the project's columns in order; the finish stage is always the last one.
func (s *Store) Stages(ctx context.Context, projectID string) ([]Stage, error) {
	if err := ensureProjectStages(ctx, s.db, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id, coalesce(code, ''), title, color FROM project_stages
		WHERE project_id = $1 ORDER BY (code IS NOT DISTINCT FROM 'finish'), sort, created_at`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Stage])
}

// stageOf places a task: its own stage, else the finish stage for finished tasks, else the first stage.
func stageOf(t Task, stages []Stage, index map[string]int) int {
	if i, ok := index[t.StageID]; ok {
		return i
	}
	if t.Status == StatusCompleted || t.Status == StatusAwaitingControl {
		return len(stages) - 1
	}
	return 0
}

// Board returns the project's kanban: the tasks the user may see, by stage.
func (s *Store) Board(ctx context.Context, userID string, p Project) ([]StageColumn, error) {
	stages, err := s.Stages(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	cols := make([]StageColumn, len(stages))
	index := map[string]int{}
	for i, st := range stages {
		cols[i].Stage = st
		index[st.ID] = i
	}
	list, err := s.listOrdered(ctx, Filter{ProjectID: p.ID, Viewer: userID},
		`t.stage_sort, t.closed_at DESC NULLS FIRST, t.created_at DESC`)
	if err != nil {
		return nil, err
	}
	sprint := ""
	if p.Scrum() { // a scrum team's kanban is the active sprint
		sp, err := s.ActiveSprint(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if sp == nil {
			return cols, nil
		}
		sprint = sp.ID
	}
	for _, t := range list {
		if p.Scrum() && t.SprintID != sprint {
			continue
		}
		i := stageOf(t, stages, index)
		cols[i].Total++
		if cols[i].Finish() && len(cols[i].Tasks) >= FinishLimit {
			continue
		}
		cols[i].Tasks = append(cols[i].Tasks, t)
	}
	return cols, nil
}

func (s *Store) stageByID(ctx context.Context, q querier, projectID, stageID string) (Stage, error) {
	var st Stage
	err := q.QueryRow(ctx, `SELECT id, coalesce(code, ''), title, color FROM project_stages WHERE id = $1 AND project_id = $2`,
		stageID, projectID).Scan(&st.ID, &st.Code, &st.Title, &st.Color)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, ErrNotFound
	}
	return st, err
}

// MoveToStage puts a task into a stage, like dragging a card on the portal kanban.
// Dropping into the finish stage completes the task (or sends it to control); dragging a finished task out
// renews it (a task awaiting control goes back to work only if its creator disapproves it). The workflow
// rules of Transition apply, so a move the user may not make changes nothing.
func (s *Store) MoveToStage(ctx context.Context, userID, taskID, stageID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !(a.Roles.Worker() || a.Roles.Manager) {
			return ErrForbidden
		}
		st, err := s.stageByID(ctx, tx, a.ProjectID, stageID)
		if err != nil {
			return err
		}
		finished := a.Status == StatusCompleted || a.Status == StatusAwaitingControl
		var act Action
		switch {
		case st.Finish() && !finished:
			act = ActionComplete
		case !st.Finish() && a.Status == StatusCompleted:
			act = ActionRenew
		case !st.Finish() && a.Status == StatusAwaitingControl:
			act = ActionDisapprove
		}
		if act != "" {
			next, err := Transition(a.Status, act, a.Roles, a.RequiresControl)
			if err != nil {
				return err
			}
			if err := setStatus(ctx, tx, taskID, userID, a.Status, next); err != nil {
				return err
			}
		}
		var oldStage string
		if err := tx.QueryRow(ctx, `SELECT coalesce(stage_id::text, '') FROM tasks WHERE id = $1`, taskID).Scan(&oldStage); err != nil {
			return err
		}
		if oldStage == stageID {
			return nil
		}
		if err := putInStage(ctx, tx, taskID, stageID); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "stage", stageName(ctx, tx, oldStage), stageName(ctx, tx, stageID))
	})
}

// putInStage moves the task to the top of the stage.
func putInStage(ctx context.Context, q querier, taskID, stageID string) error {
	_, err := q.Exec(ctx, `UPDATE tasks SET stage_id = $2::uuid,
		stage_sort = (SELECT coalesce(min(stage_sort), 0) - 1 FROM tasks WHERE stage_id = $2::uuid),
		updated_at = now() WHERE id = $1`, taskID, stageID)
	return err
}

// stageName is stored in history: the custom title, or "stage.<code>" for the UI to translate.
func stageName(ctx context.Context, q querier, stageID string) string {
	if stageID == "" {
		return ""
	}
	var code, title string
	if err := q.QueryRow(ctx, `SELECT coalesce(code, ''), title FROM project_stages WHERE id = $1`, stageID).Scan(&code, &title); err != nil {
		return ""
	}
	if title == "" && code != "" {
		return "stage." + code
	}
	return title
}

// syncStage keeps the kanban in step with status changes made elsewhere (task card, API):
// a finished task goes to the finish stage, a reopened one leaves it for the first stage.
// Projects that never opened their kanban have no stages yet and are left alone.
func syncStage(ctx context.Context, q querier, projectID, taskID string, next Status) error {
	if next == StatusCompleted || next == StatusAwaitingControl {
		_, err := q.Exec(ctx, `UPDATE tasks SET stage_id = f.id,
			stage_sort = (SELECT coalesce(min(stage_sort), 0) - 1 FROM tasks WHERE stage_id = f.id)
			FROM project_stages f WHERE f.project_id = $1 AND f.code = 'finish' AND tasks.id = $2
			  AND tasks.stage_id IS DISTINCT FROM f.id`, projectID, taskID)
		return err
	}
	_, err := q.Exec(ctx, `UPDATE tasks SET stage_id = (SELECT id FROM project_stages WHERE project_id = $1
			AND code IS DISTINCT FROM 'finish' ORDER BY sort, created_at LIMIT 1)
		WHERE id = $2 AND stage_id IN (SELECT id FROM project_stages WHERE code = 'finish')`, projectID, taskID)
	return err
}

// ---------- stage management (project owner/manager) ----------

func normColor(c string) (string, error) {
	c = strings.TrimSpace(c)
	if c == "" {
		return StageColors[0], nil
	}
	if !colorRe.MatchString(c) {
		return "", invalid("err.stage_color")
	}
	return strings.ToUpper(strings.TrimPrefix(c, "#")), nil
}

// AddProjectStage adds a column before the finish stage.
func (s *Store) AddProjectStage(ctx context.Context, p Project, title, color string) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return invalid("err.stage_title_required")
	}
	color, err := normColor(color)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := ensureProjectStages(ctx, tx, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_stages (project_id, title, color, sort)
			VALUES ($1, $2, $3, (SELECT coalesce(max(sort), 0) + 10 FROM project_stages WHERE project_id = $1 AND code IS DISTINCT FROM 'finish'))`,
			p.ID, title, color); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE project_stages SET sort = (SELECT max(sort) + 10 FROM project_stages WHERE project_id = $1)
			WHERE project_id = $1 AND code = 'finish'`, p.ID)
		return err
	})
}

// UpdateProjectStage renames and recolors a column (a built-in stage keeps its role).
func (s *Store) UpdateProjectStage(ctx context.Context, p Project, stageID, title, color string) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return invalid("err.stage_title_required")
	}
	color, err := normColor(color)
	if err != nil {
		return err
	}
	if _, err := s.stageByID(ctx, s.db, p.ID, stageID); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE project_stages SET title = $2, color = $3 WHERE id = $1`, stageID, title, color)
	return err
}

// DeleteProjectStage removes an empty column. The finish stage and the last working stage stay.
func (s *Store) DeleteProjectStage(ctx context.Context, p Project, stageID string) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		st, err := s.stageByID(ctx, tx, p.ID, stageID)
		if err != nil {
			return err
		}
		if st.Finish() {
			return invalid("err.stage_finish_fixed")
		}
		var others, used int
		if err := tx.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM project_stages WHERE project_id = $1 AND id <> $2 AND code IS DISTINCT FROM 'finish'),
				(SELECT count(*) FROM tasks WHERE stage_id = $2)`, p.ID, stageID).Scan(&others, &used); err != nil {
			return err
		}
		if others == 0 {
			return invalid("err.stage_last")
		}
		if used > 0 {
			return invalid("err.stage_not_empty", used)
		}
		_, err = tx.Exec(ctx, `DELETE FROM project_stages WHERE id = $1`, stageID)
		return err
	})
}

// ShiftProjectStage moves a working column one place left (dir < 0) or right; the finish stage stays last.
func (s *Store) ShiftProjectStage(ctx context.Context, p Project, stageID string, dir int) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM project_stages WHERE project_id = $1 AND code IS DISTINCT FROM 'finish'
			ORDER BY sort, created_at FOR UPDATE`, p.ID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		i := -1
		for k, id := range ids {
			if id == stageID {
				i = k
			}
		}
		if i < 0 {
			return ErrNotFound
		}
		j := i + 1
		if dir < 0 {
			j = i - 1
		}
		if j < 0 || j >= len(ids) {
			return nil
		}
		ids[i], ids[j] = ids[j], ids[i]
		for k, id := range ids {
			if _, err := tx.Exec(ctx, `UPDATE project_stages SET sort = $2 WHERE id = $1`, id, (k+1)*10); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE project_stages SET sort = $2 WHERE project_id = $1 AND code = 'finish'`, p.ID, (len(ids)+1)*10)
		return err
	})
}

// CardInfo is what a kanban card shows besides the task row.
type CardInfo struct {
	ChecklistDone, ChecklistTotal, Files, Comments int
}

func (s *Store) CardInfo(ctx context.Context, ids []string) (map[string]CardInfo, error) {
	out := map[string]CardInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT t.id::text,
			(SELECT count(*) FILTER (WHERE done) FROM task_checklist_items c WHERE c.task_id = t.id)::int,
			(SELECT count(*) FROM task_checklist_items c WHERE c.task_id = t.id)::int,
			(SELECT count(*) FROM files f WHERE f.task_id = t.id)::int,
			(SELECT count(*) FROM task_comments m WHERE m.task_id = t.id)::int
		FROM tasks t WHERE t.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var c CardInfo
		if err := rows.Scan(&id, &c.ChecklistDone, &c.ChecklistTotal, &c.Files, &c.Comments); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}
