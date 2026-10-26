package tasks

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Task trash (Корзина): a deleted task goes to the trash with its subtasks and everything it has
// (comments, checklist, files, time, history) — the rows stay, only «deleted_at» hides them (the «tasks» view).
// The creator deletes own tasks, administrators any; the trash lists what the user created or deleted (everything
// in admin mode). A task comes back as it was or is deleted for good, by hand or after the days of the company
// setting (0 — kept until deleted by hand).

const settingTrashDays = "task_trash_days"

// DefaultTrashDays keeps deleted tasks a month, as the portal recycle bin does.
const DefaultTrashDays = 30

// TrashItem is a deleted task (the one the user deleted; its subtasks are counted).
type TrashItem struct {
	ID, Key, Title, ProjectKey, ProjectName string
	CreatorName, DeletedByName              string
	DeletedAt                               time.Time
	Subtasks                                int
	PurgeAt                                 *time.Time `db:"-"` // when it goes for good (nil — kept)
}

// subtreeSQL selects the live task $1 and its live subtasks of any depth.
const subtreeSQL = `WITH RECURSIVE sub AS (SELECT id FROM tasks WHERE id = $1
	UNION SELECT t.id FROM tasks t JOIN sub ON t.parent_id = sub.id) SELECT id FROM sub`

// DeleteTask moves the task with its subtasks to the trash.
func (s *Store) DeleteTask(ctx context.Context, userID, taskID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !a.CanDelete {
			return ErrForbidden
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks_all SET deleted_at = now(), deleted_by = $2, deleted_root = $1
			WHERE id IN (`+subtreeSQL+`)`, taskID, userID); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "deleted", "", "")
	})
}

// trashRoot checks that the task is a deleted one the user may restore or purge.
func trashRoot(ctx context.Context, q querier, userID, taskID string, forUpdate bool) error {
	sql := `SELECT creator_id::text, coalesce(deleted_by::text, '') FROM tasks_all
		WHERE id = $1 AND deleted_at IS NOT NULL AND deleted_root = id`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	var creator, by string
	err := q.QueryRow(ctx, sql, taskID).Scan(&creator, &by)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if creator != userID && by != userID && !IsAdmin(ctx) {
		return ErrForbidden
	}
	return nil
}

// Trash lists the deleted tasks the user sees, the latest first.
func (s *Store) Trash(ctx context.Context, userID string) ([]TrashItem, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id::text, p.key || '-' || t.number, t.title, p.key, p.name, cu.name, coalesce(du.name, ''),
		       t.deleted_at, (SELECT count(*) FROM tasks_all c WHERE c.deleted_root = t.id AND c.id <> t.id AND c.deleted_at IS NOT NULL)
		FROM tasks_all t JOIN projects p ON p.id = t.project_id JOIN users cu ON cu.id = t.creator_id
		LEFT JOIN users du ON du.id = t.deleted_by
		WHERE t.deleted_at IS NOT NULL AND t.deleted_root = t.id AND ($2 OR t.creator_id = $1 OR t.deleted_by = $1)
		ORDER BY t.deleted_at DESC LIMIT $3`, userID, IsAdmin(ctx), ListLimit)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[TrashItem])
	if err != nil {
		return nil, err
	}
	days, err := s.TrashDays(ctx)
	if err != nil {
		return nil, err
	}
	if days > 0 {
		for i := range list {
			at := list[i].DeletedAt.AddDate(0, 0, days)
			list[i].PurgeAt = &at
		}
	}
	return list, nil
}

// RestoreTask brings the deleted task back with its subtasks. A task whose parent is in the trash comes back on its own.
func (s *Store) RestoreTask(ctx context.Context, userID, taskID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := trashRoot(ctx, tx, userID, taskID, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks_all SET deleted_at = NULL, deleted_by = NULL, deleted_root = NULL
			WHERE deleted_root = $1 AND deleted_at IS NOT NULL`, taskID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks_all t SET parent_id = NULL FROM tasks_all pt
			WHERE t.id = $1 AND pt.id = t.parent_id AND pt.deleted_at IS NOT NULL`, taskID); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "restored", "", "")
	})
}

// PurgeTask deletes the task from the trash for good, with its subtasks; the files' bytes are left to the storage's cleanup.
func (s *Store) PurgeTask(ctx context.Context, userID, taskID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := trashRoot(ctx, tx, userID, taskID, true); err != nil {
			return err
		}
		return purge(ctx, tx, taskID)
	})
}

func purge(ctx context.Context, tx pgx.Tx, root string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM files WHERE task_id IN (SELECT id FROM tasks_all WHERE deleted_root = $1 AND deleted_at IS NOT NULL)`,
		root); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM tasks_all WHERE deleted_root = $1 AND deleted_at IS NOT NULL`, root)
	return err
}

// PurgeExpired deletes for good what has been in the trash longer than the company setting allows.
func (s *Store) PurgeExpired(ctx context.Context, now time.Time) (int, error) {
	days, err := s.TrashDays(ctx)
	if err != nil || days == 0 {
		return 0, err
	}
	n := 0
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM tasks_all WHERE deleted_at IS NOT NULL AND deleted_root = id
			AND deleted_at < $1 FOR UPDATE SKIP LOCKED`, now.AddDate(0, 0, -days))
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := purge(ctx, tx, id); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// TrashDays is how many days deleted tasks are kept (0 — until deleted by hand).
func (s *Store) TrashDays(ctx context.Context) (int, error) {
	var v string
	err := s.db.QueryRow(ctx, `SELECT value #>> '{}' FROM company_settings WHERE key = $1`, settingTrashDays).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultTrashDays, nil
	}
	if err != nil {
		return 0, err
	}
	d, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || d < 0 {
		return DefaultTrashDays, nil
	}
	return d, nil
}

// SetTrashDays saves the setting. Only administrators call it (checked by the web layer).
func (s *Store) SetTrashDays(ctx context.Context, actorID string, days int) error {
	if days < 0 || days > 3650 {
		return invalid("err.trash_days")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, to_jsonb($2::int), $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingTrashDays, days, actorID)
	return err
}

// TaskByIDAll loads one task row whether or not it is in the trash (the messenger logs the deletion to its thread).
func (s *Store) TaskByIDAll(ctx context.Context, taskID string) (Task, error) {
	t, err := scanTask(s.db.QueryRow(ctx, strings.Replace(taskSelect, "FROM tasks t", "FROM tasks_all t", 1)+" WHERE t.id = $1", taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}
