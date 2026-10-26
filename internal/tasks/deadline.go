package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/worktime"
)

const settingWorktime = "worktime"

// Schedule is the company work schedule (defaults until an administrator saves one).
func (s *Store) Schedule(ctx context.Context) (worktime.Schedule, error) {
	sched := worktime.Default()
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingWorktime).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return sched, nil
	}
	if err != nil {
		return sched, err
	}
	if err := json.Unmarshal(raw, &sched); err != nil {
		return worktime.Default(), nil
	}
	return sched, nil
}

// SetSchedule saves the work schedule. Only administrators call it (checked by the web layer).
func (s *Store) SetSchedule(ctx context.Context, actorID string, sched worktime.Schedule) error {
	if err := sched.Validate(); err != nil {
		return invalid("err.worktime")
	}
	raw, _ := json.Marshal(sched)
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingWorktime, raw, actorID)
	return err
}

// SetDeadline moves the deadline from the task card. The creator and project managers always may;
// the responsible only when the task allows it («Разрешить исполнителю менять сроки»).
func (s *Store) SetDeadline(ctx context.Context, userID, taskID string, deadline *time.Time) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		var cur *time.Time
		var allow, match, follow bool
		if err := tx.QueryRow(ctx, `SELECT deadline, allow_change_deadline, match_work_time,
			dates_from_subtasks AND EXISTS (SELECT 1 FROM tasks c WHERE c.parent_id = t.id) FROM tasks t WHERE id = $1`, taskID).
			Scan(&cur, &allow, &match, &follow); err != nil {
			return err
		}
		if follow {
			return invalid("err.deadline_from_subtasks")
		}
		if !(a.Roles.Creator || a.Roles.Manager || (a.Roles.Responsible && allow)) {
			return invalid("err.deadline_locked")
		}
		if match {
			sched, err := s.Schedule(ctx)
			if err != nil {
				return err
			}
			deadline = sched.Adjust(deadline)
		}
		if fmtTime(cur) == fmtTime(deadline) {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET deadline = $2, version = version + 1, updated_at = now() WHERE id = $1`,
			taskID, deadline); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "deadline", fmtTime(cur), fmtTime(deadline))
	})
}

// CanChangeDeadline is what the card uses to offer the deadline editor.
func (c TaskCard) CanChangeDeadline() bool {
	return !c.DatesFollow() && (c.Roles.Creator || c.Roles.Manager || (c.Roles.Responsible && c.AllowChangeDeadline))
}
