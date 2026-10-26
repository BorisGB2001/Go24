package tasks

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		day := func(x time.Time) time.Time {
			y, m, d := x.Local().Date()
			return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		}
		switch {
		case t.StartPlan != nil:
			gt.Start = *t.StartPlan
		default:
			gt.Start = day(t.CreatedAt)
		}
		switch {
		case t.EndPlan != nil:
			gt.End = *t.EndPlan
		case t.Deadline != nil:
			gt.End = day(*t.Deadline)
		case t.ClosedAt != nil:
			gt.End = day(*t.ClosedAt)
		default:
			gt.End = gt.Start
		}
		if gt.End.Before(gt.Start) {
			gt.End = gt.Start
		}
		g.Tasks = append(g.Tasks, gt)
	}
	g.Links, err = scanLinks(ctx, s.db, "f.project_id = $1 AND t.project_id = $1", p.ID)
	return g, err
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
