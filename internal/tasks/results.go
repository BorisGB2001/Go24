package tasks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// «Оценка» and «Результат».

// Rate: the creator (or a project manager) rates the task — positive, negative or "" (no rating).
func (s *Store) Rate(ctx context.Context, userID, taskID, rating string) error {
	if rating != "" && rating != "positive" && rating != "negative" {
		return invalid("err.bad_form")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !(a.Roles.Creator || a.Roles.Manager) {
			return ErrForbidden
		}
		var old string
		if err := tx.QueryRow(ctx, `SELECT rating FROM tasks WHERE id = $1`, taskID).Scan(&old); err != nil {
			return err
		}
		if old == rating {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET rating = $2, updated_at = now() WHERE id = $1`, taskID, rating); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "rating", old, rating)
	})
}

// Result is a chat message marked as the task's result.
type Result struct {
	Comment
	MarkedBy string
	MarkedAt time.Time
}

// SetResult marks a chat message as the task's result (or unmarks it): the responsible, accomplices, the creator
// or a project manager.
func (s *Store) SetResult(ctx context.Context, userID, taskID, commentID string, on bool) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, false)
		if err != nil {
			return err
		}
		if !(a.Roles.Worker() || a.Roles.Creator || a.Roles.Manager) {
			return ErrForbidden
		}
		var body string
		if err := tx.QueryRow(ctx, `SELECT left(body, 200) FROM task_comments WHERE id = $1 AND task_id = $2`, commentID, taskID).Scan(&body); err != nil {
			return ErrNotFound
		}
		if on {
			tag, err := tx.Exec(ctx, `INSERT INTO task_results (task_id, comment_id, created_by) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				taskID, commentID, userID)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			return addHistory(ctx, tx, taskID, userID, "result", "", body)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM task_results WHERE task_id = $1 AND comment_id = $2`, taskID, commentID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "result", body, "")
	})
}

// Results are the task's result messages, oldest first.
func (s *Store) Results(ctx context.Context, taskID string) ([]Result, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id, c.author_id, u.name, c.body, c.created_at, mu.name, r.created_at
		FROM task_results r JOIN task_comments c ON c.id = r.comment_id JOIN users u ON u.id = c.author_id JOIN users mu ON mu.id = r.created_by
		WHERE r.task_id = $1 ORDER BY c.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Result, error) {
		var x Result
		err := r.Scan(&x.ID, &x.AuthorID, &x.AuthorName, &x.Body, &x.CreatedAt, &x.MarkedBy, &x.MarkedAt)
		return x, err
	})
}
