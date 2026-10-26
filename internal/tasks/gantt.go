package tasks

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/worktime"
)

// Link is a dependency of To on From, of one of the LinkTypes: finish_start — To cannot start before From
// finishes (the default), start_start, finish_finish, start_finish.
type Link struct {
	ID        string
	Type      string
	FromID    string
	ToID      string
	FromKey   string
	ToKey     string
	FromTitle string
	ToTitle   string
}

// LinkTypes are the dependency kinds Gantt: the side of the predecessor, then of the successor.
var LinkTypes = []string{"finish_start", "start_start", "finish_finish", "start_finish"}

func validLinkType(t string) bool { return slices.Contains(LinkTypes, t) }

const linkSelect = `
	SELECT l.id, l.type, l.from_task, l.to_task, fp.key || '-' || f.number, tp.key || '-' || t.number, f.title, t.title
	FROM task_links l
	JOIN tasks f ON f.id = l.from_task JOIN projects fp ON fp.id = f.project_id
	JOIN tasks t ON t.id = l.to_task JOIN projects tp ON tp.id = t.project_id`

func scanLinks(ctx context.Context, q querier, where string, args ...any) ([]Link, error) {
	rows, err := q.Query(ctx, linkSelect+" WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.Type, &l.FromID, &l.ToID, &l.FromKey, &l.ToKey, &l.FromTitle, &l.ToTitle); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// TaskLinks returns the task's predecessors and successors.
func (s *Store) TaskLinks(ctx context.Context, taskID string) (pred, succ []Link, err error) {
	if pred, err = scanLinks(ctx, s.db, "l.to_task = $1 ORDER BY f.number", taskID); err != nil {
		return nil, nil, err
	}
	succ, err = scanLinks(ctx, s.db, "l.from_task = $1 ORDER BY t.number", taskID)
	return pred, succ, err
}

// AddLink makes predKey a predecessor of the task with a link of the type ("" — finish_start). The user must be able
// to edit the task and see the predecessor; links that would form a cycle are refused; linking the pair again
// changes the type.
func (s *Store) AddLink(ctx context.Context, userID, taskID, predKey, typ string) error {
	if typ == "" {
		typ = "finish_start"
	}
	if !validLinkType(typ) {
		return invalid("err.link_type")
	}
	predID, err := s.TaskIDByKey(ctx, predKey)
	if err != nil {
		return invalid("err.link_not_found", predKey)
	}
	if predID == taskID {
		return invalid("err.link_self")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, false)
		if err != nil {
			return err
		}
		if !(a.Roles.Creator || a.Roles.Responsible || a.Roles.Manager) {
			return ErrForbidden
		}
		if _, err := loadAccess(ctx, tx, userID, predID, false); err != nil {
			return err
		}
		// A cycle appears if the predecessor is already reachable from the task through successor links.
		var cycle bool
		if err := tx.QueryRow(ctx, `
			WITH RECURSIVE down(id) AS (
				SELECT to_task FROM task_links WHERE from_task = $1
				UNION SELECT l.to_task FROM task_links l JOIN down d ON l.from_task = d.id
			) SELECT EXISTS (SELECT 1 FROM down WHERE id = $2)`, taskID, predID).Scan(&cycle); err != nil {
			return err
		}
		if cycle {
			return invalid("err.link_cycle")
		}
		var old string
		err = tx.QueryRow(ctx, `SELECT type FROM task_links WHERE from_task = $1 AND to_task = $2`, predID, taskID).Scan(&old)
		switch {
		case err == nil && old == typ:
			return nil // already linked
		case err == nil:
			_, err = tx.Exec(ctx, `UPDATE task_links SET type = $3 WHERE from_task = $1 AND to_task = $2`, predID, taskID, typ)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO task_links (from_task, to_task, type, created_by) VALUES ($1, $2, $3, $4)`, predID, taskID, typ, userID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil // linked meanwhile
		}
		if err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "predecessor", "", predKey)
	})
}

// RemoveLink deletes a dependency of a task the user can edit.
func (s *Store) RemoveLink(ctx context.Context, userID, taskID, linkID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, false)
		if err != nil {
			return err
		}
		if !(a.Roles.Creator || a.Roles.Responsible || a.Roles.Manager) {
			return ErrForbidden
		}
		var predKey string
		err = tx.QueryRow(ctx, `DELETE FROM task_links l USING tasks f, projects p
			WHERE l.id = $1 AND (l.to_task = $2 OR l.from_task = $2) AND f.id = l.from_task AND p.id = f.project_id
			RETURNING p.key || '-' || f.number`, linkID, taskID).Scan(&predKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "predecessor", predKey, "")
	})
}

// GanttData is a project's tasks with their bar dates and the links between them.
type GanttData struct {
	Tasks []GanttTask
	Links []Link
}

type GanttTask struct {
	Task
	Start, End time.Time // calendar dates of the bar (inclusive)
	Planned    bool      // dates come from start/end plan (not derived)
	Editable   bool      // the viewer moves the bar (edits the task, its dates do not follow the subtasks)
	Linkable   bool      // the viewer adds predecessors to the task (edits it)
}

// Gantt builds bars for the project: plan dates, else creation date → deadline; open tasks first by start.
func (s *Store) Gantt(ctx context.Context, viewerID string, p Project, withCompleted bool) (GanttData, error) {
	var g GanttData
	list, err := s.ListTasks(ctx, Filter{ProjectID: p.ID, OnlyOpen: !withCompleted, Viewer: viewerID})
	if err != nil {
		return g, err
	}
	perms, err := permsOf(ctx, s.db, viewerID)
	if err != nil {
		return g, err
	}
	all := IsAdmin(ctx) || (perms.Has("edit", "all") && !p.Collab()) || (p.CanManage() && !p.Archived)
	full := perms.Has("edit", "sub") // heads and those acting for someone: the full check of each task
	if !full {
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM absences
			WHERE acting_id = $1 AND current_date BETWEEN starts_on AND ends_on)`, viewerID).Scan(&full); err != nil {
			return g, err
		}
	}
	for _, t := range list {
		gt := GanttTask{Task: t, Planned: t.StartPlan != nil || t.EndPlan != nil}
		switch {
		case all || t.CreatorID == viewerID:
			gt.Linkable = true
		case full:
			if a, err := loadAccess(ctx, s.db, viewerID, t.ID, false); err == nil {
				gt.Linkable = a.Roles.Creator || a.Roles.Manager
			}
		}
		gt.Editable = gt.Linkable && !t.DatesFollow() && !p.Frozen()
		gt.Start, gt.End = barDates(t.StartPlan, t.EndPlan, t.CreatedAt, t.Deadline, t.ClosedAt)
		g.Tasks = append(g.Tasks, gt)
	}
	g.Links, err = scanLinks(ctx, s.db, "f.project_id = $1 AND t.project_id = $1", p.ID)
	return g, err
}

// barDates is the span of a task's bar: the plan dates, else the creation date → the deadline (or the completion).
func barDates(startPlan, endPlan *time.Time, created time.Time, deadline, closed *time.Time) (start, end time.Time) {
	day := func(x time.Time) time.Time {
		y, m, d := x.Local().Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	switch {
	case startPlan != nil:
		start = *startPlan
	default:
		start = day(created)
	}
	switch {
	case endPlan != nil:
		end = *endPlan
	case deadline != nil:
		end = day(*deadline)
	case closed != nil:
		end = day(*closed)
	default:
		end = start
	}
	if end.Before(start) {
		end = start
	}
	return start, end
}

// linkShift is how many days the successor's bar (ss..se) must move later for the link of the type to the
// predecessor's bar (ps..pe) to hold; 0 — it holds.
func linkShift(typ string, ps, pe, ss, se time.Time) int {
	need, have := pe.AddDate(0, 0, 1), ss // finish_start: the successor starts the day after
	switch typ {
	case "start_start":
		need, have = ps, ss
	case "finish_finish":
		need, have = pe, se
	case "start_finish":
		need, have = ps, se
	}
	if !have.Before(need) {
		return 0
	}
	return int(need.Sub(have).Hours()/24 + 0.5)
}

// shiftSuccessors moves the plan dates of the task's successors later, keeping their length, where the task's new
// dates break a link — Gantt shifts the dependent tasks — and so on down the chain. Completed
// successors, those the user cannot edit and those whose dates follow their subtasks stay; returns the moved ones.
func (s *Store) shiftSuccessors(ctx context.Context, tx pgx.Tx, userID, taskID string) (int, error) {
	type bar struct {
		start, end time.Time
	}
	barOf := func(id string) (b bar, open, movable bool, workTime bool, err error) {
		var sp, ep, dl, cl *time.Time
		var created time.Time
		var st Status
		var follow bool
		err = tx.QueryRow(ctx, `SELECT start_plan, end_plan, created_at, deadline, closed_at, status, match_work_time,
			dates_from_subtasks AND EXISTS (SELECT 1 FROM tasks c WHERE c.parent_id = t.id)
			FROM tasks t WHERE id = $1`, id).Scan(&sp, &ep, &created, &dl, &cl, &st, &workTime, &follow)
		b.start, b.end = barDates(sp, ep, created, dl, cl)
		return b, !st.Closed(), !follow, workTime, err
	}
	var sched *worktime.Schedule
	moved, queue := 0, []string{taskID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		pred, _, _, _, err := barOf(id)
		if err != nil {
			return moved, err
		}
		rows, err := tx.Query(ctx, `SELECT to_task, type FROM task_links WHERE from_task = $1`, id)
		if err != nil {
			return moved, err
		}
		type succ struct{ id, typ string }
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (succ, error) {
			var x succ
			return x, r.Scan(&x.id, &x.typ)
		})
		if err != nil {
			return moved, err
		}
		for _, l := range list {
			b, open, movable, workTime, err := barOf(l.id)
			if err != nil {
				return moved, err
			}
			days := linkShift(l.typ, pred.start, pred.end, b.start, b.end)
			if days == 0 || !open || !movable {
				continue
			}
			if a, err := loadAccess(ctx, tx, userID, l.id, true); err != nil || !(a.Roles.Creator || a.Roles.Manager) {
				continue
			}
			ns, ne := b.start.AddDate(0, 0, days), b.end.AddDate(0, 0, days)
			if workTime {
				if sched == nil {
					sc, err := s.Schedule(ctx)
					if err != nil {
						return moved, err
					}
					sched = &sc
				}
				ns = *sched.AdjustDay(&ns)
				if ne = *sched.AdjustDay(&ne); ne.Before(ns) {
					ne = ns
				}
			}
			var oldS, oldE *time.Time
			if err := tx.QueryRow(ctx, `SELECT start_plan, end_plan FROM tasks WHERE id = $1`, l.id).Scan(&oldS, &oldE); err != nil {
				return moved, err
			}
			if _, err := tx.Exec(ctx, `UPDATE tasks SET start_plan = $2, end_plan = $3, version = version + 1, updated_at = now()
				WHERE id = $1`, l.id, ns, ne); err != nil {
				return moved, err
			}
			if err := addHistory(ctx, tx, l.id, userID, "start_plan", dateText(oldS), dateText(&ns)); err != nil {
				return moved, err
			}
			if err := addHistory(ctx, tx, l.id, userID, "end_plan", dateText(oldE), dateText(&ne)); err != nil {
				return moved, err
			}
			moved++
			queue = append(queue, l.id)
		}
	}
	return moved, nil
}

// SetPlanDates moves the task's plan dates (the bar of the Gantt chart), keeping its other fields.
func (s *Store) SetPlanDates(ctx context.Context, userID, taskID string, start, end *time.Time) error {
	c, err := s.GetCard(ctx, userID, taskID)
	if err != nil {
		return err
	}
	if c.DatesFollow() {
		return invalid("err.dates_from_subtasks")
	}
	return s.UpdateTask(ctx, userID, taskID, TaskUpdate{Version: c.Version, Title: c.Title, Description: c.Description,
		ResponsibleID: c.ResponsibleID, Priority: c.Priority, Deadline: c.Deadline, RequiresControl: c.RequiresControl,
		EstimateMinutes: c.EstimateMinutes, StartPlan: start, EndPlan: end,
		AllowChangeDeadline: c.AllowChangeDeadline, MatchWorkTime: c.MatchWorkTime, Params: c.Params})
}
