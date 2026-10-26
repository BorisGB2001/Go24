package tasks

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Link is a finish-to-start dependency: To cannot start before From finishes.
type Link struct {
	ID        string
	FromID    string
	ToID      string
	FromKey   string
	ToKey     string
	FromTitle string
	ToTitle   string
}

const linkSelect = `
	SELECT l.id, l.from_task, l.to_task, fp.key || '-' || f.number, tp.key || '-' || t.number, f.title, t.title
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
		if err := rows.Scan(&l.ID, &l.FromID, &l.ToID, &l.FromKey, &l.ToKey, &l.FromTitle, &l.ToTitle); err != nil {
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

// AddLink makes predKey a predecessor of the task. The user must be able to edit the task and see the predecessor;
// links that would form a cycle are refused.
func (s *Store) AddLink(ctx context.Context, userID, taskID, predKey string) error {
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
		_, err = tx.Exec(ctx, `INSERT INTO task_links (from_task, to_task, created_by) VALUES ($1, $2, $3)`, predID, taskID, userID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil // already linked
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
}

// Gantt builds bars for the project: plan dates, else creation date → deadline; open tasks first by start.
func (s *Store) Gantt(ctx context.Context, viewerID, projectID string, withCompleted bool) (GanttData, error) {
	var g GanttData
	list, err := s.ListTasks(ctx, Filter{ProjectID: projectID, OnlyOpen: !withCompleted, Viewer: viewerID})
	if err != nil {
		return g, err
	}
	for _, t := range list {
		gt := GanttTask{Task: t, Planned: t.StartPlan != nil || t.EndPlan != nil}
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
	g.Links, err = scanLinks(ctx, s.db, "f.project_id = $1 AND t.project_id = $1", projectID)
	return g, err
}
