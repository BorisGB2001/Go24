package tasks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// «Просмотры»: opening a task's card counts as a view of the person; the card shows how many people
// saw the task and, on a click, who and when.

// TaskViewer is one who opened the task: the first and the last time, and how many times.
type TaskViewer struct {
	ID, Name, Photo string
	First, Last     time.Time
	Times           int
}

// See counts a view of the task by the person: the card was opened (the caller has checked the access).
func (s *Store) See(ctx context.Context, userID, taskID string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO task_views (task_id, user_id) VALUES ($1, $2)
		ON CONFLICT (task_id, user_id) DO UPDATE SET viewed_at = now(), times = task_views.times + 1`, taskID, userID)
	return err
}

// Viewers: who saw the task, the latest view first (for anyone who sees the task).
func (s *Store) Viewers(ctx context.Context, userID, taskID string) ([]TaskViewer, error) {
	if err := s.CheckVisible(ctx, userID, taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT u.id, u.name, u.photo_key, v.first_at, v.viewed_at, v.times
		FROM task_views v JOIN users u ON u.id = v.user_id WHERE v.task_id = $1 ORDER BY v.viewed_at DESC, u.name`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[TaskViewer])
}
