package tasks

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PlanColumn is a column of «Мой план». ID "" is the virtual «unplanned» column.
type PlanColumn struct {
	ID    string
	Code  string // today | week | later for built-in columns, "" for custom ones
	Title string // custom title
	Tasks []Task
}

// ensureStages creates the built-in columns on the first visit.
func (s *Store) ensureStages(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO plan_stages (user_id, code, sort)
		SELECT $1, c.code, c.sort FROM (VALUES ('today', 10), ('week', 20), ('later', 30)) AS c(code, sort)
		WHERE NOT EXISTS (SELECT 1 FROM plan_stages WHERE user_id = $1)`, userID)
	return err
}

// Plan returns the user's board: open tasks where the user is responsible or an accomplice.
func (s *Store) Plan(ctx context.Context, userID string) ([]PlanColumn, error) {
	if err := s.ensureStages(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id, coalesce(code, ''), title FROM plan_stages WHERE user_id = $1 ORDER BY sort, created_at`, userID)
	if err != nil {
		return nil, err
	}
	cols := []PlanColumn{{}}
	index := map[string]int{"": 0}
	for rows.Next() {
		var c PlanColumn
		if err := rows.Scan(&c.ID, &c.Code, &c.Title); err != nil {
			rows.Close()
			return nil, err
		}
		index[c.ID] = len(cols)
		cols = append(cols, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.Query(ctx, taskSelect+`
		LEFT JOIN plan_items pi ON pi.task_id = t.id AND pi.user_id = $1
		WHERE t.status <> 'completed'
		  AND (t.responsible_id = $1 OR EXISTS (SELECT 1 FROM task_participants tp
		       WHERE tp.task_id = t.id AND tp.user_id = $1 AND tp.role = 'accomplice'))
		ORDER BY pi.sort NULLS LAST, (t.deadline IS NULL), t.deadline, t.priority DESC, t.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// taskSelect has a fixed column list, so read the stage separately per task id.
	var list []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stageOf := map[string]string{}
	srows, err := s.db.Query(ctx, `SELECT task_id::text, stage_id::text FROM plan_items WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var tid, sid string
		if err := srows.Scan(&tid, &sid); err != nil {
			srows.Close()
			return nil, err
		}
		stageOf[tid] = sid
	}
	srows.Close()
	for _, t := range list {
		i, ok := index[stageOf[t.ID]]
		if !ok {
			i = 0
		}
		cols[i].Tasks = append(cols[i].Tasks, t)
	}
	return cols, nil
}

// MovePlan puts a task into one of the user's columns (stageID "" = unplanned, at the end of the column).
func (s *Store) MovePlan(ctx context.Context, userID, taskID, stageID string) error {
	if _, err := loadAccess(ctx, s.db, userID, taskID, false); err != nil {
		return err
	}
	if stageID == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM plan_items WHERE user_id = $1 AND task_id = $2`, userID, taskID)
		return err
	}
	if err := s.ownStage(ctx, userID, stageID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO plan_items (user_id, task_id, stage_id, sort)
		VALUES ($1, $2, $3, (SELECT coalesce(max(sort), 0) + 1 FROM plan_items WHERE user_id = $1 AND stage_id = $3))
		ON CONFLICT (user_id, task_id) DO UPDATE SET stage_id = EXCLUDED.stage_id, sort = EXCLUDED.sort`, userID, taskID, stageID)
	return err
}

func (s *Store) ownStage(ctx context.Context, userID, stageID string) error {
	var owner string
	err := s.db.QueryRow(ctx, `SELECT user_id FROM plan_stages WHERE id = $1`, stageID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != userID { // plans are personal: not even admin mode opens someone else's plan
		return ErrForbidden
	}
	return nil
}

func (s *Store) AddStage(ctx context.Context, userID, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return invalid("err.stage_title_required")
	}
	if err := s.ensureStages(ctx, userID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO plan_stages (user_id, title, sort)
		VALUES ($1, $2, (SELECT coalesce(max(sort), 0) + 10 FROM plan_stages WHERE user_id = $1))`, userID, title)
	return err
}

// RenameStage gives a column a custom title (a built-in column becomes custom).
func (s *Store) RenameStage(ctx context.Context, userID, stageID, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return invalid("err.stage_title_required")
	}
	if err := s.ownStage(ctx, userID, stageID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE plan_stages SET title = $2, code = NULL WHERE id = $1`, stageID, title)
	return err
}

// DeleteStage removes a column; its tasks become unplanned.
func (s *Store) DeleteStage(ctx context.Context, userID, stageID string) error {
	if err := s.ownStage(ctx, userID, stageID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM plan_stages WHERE id = $1`, stageID)
	return err
}
