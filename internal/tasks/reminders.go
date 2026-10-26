package tasks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// «Напоминания»: each person keeps their own reminders on a task — at a time or some minutes before the
// deadline, for themselves, the responsible or the creator. The bell (and its messenger copy) delivers them.

var reminderRecipients = map[string]bool{"self": true, "responsible": true, "creator": true}

// Reminder is one reminder of the person on a task.
type Reminder struct {
	ID            string
	Recipient     string     // self | responsible | creator
	At            *time.Time // a fixed time, or
	BeforeMinutes int        // minutes before the deadline
	Due           *time.Time // when it fires (nil: before a deadline the task does not have)
	Sent          *time.Time
}

// AddReminder sets a reminder on a task the person sees: at (a time in the future) or before (minutes before the
// deadline).
func (s *Store) AddReminder(ctx context.Context, userID, taskID, recipient string, at *time.Time, before int, now time.Time) error {
	if !reminderRecipients[recipient] || (at == nil) == (before <= 0) {
		return invalid("err.bad_form")
	}
	if at != nil && !at.After(now) {
		return invalid("err.reminder_past")
	}
	if err := s.CheckVisible(ctx, userID, taskID); err != nil {
		return err
	}
	var minutes *int
	if before > 0 {
		minutes = &before
	}
	_, err := s.db.Exec(ctx, `INSERT INTO task_reminders (task_id, user_id, recipient, remind_at, before_minutes)
		VALUES ($1, $2, $3, $4, $5)`, taskID, userID, recipient, at, minutes)
	return err
}

// DeleteReminder removes one of the person's own reminders.
func (s *Store) DeleteReminder(ctx context.Context, userID, taskID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM task_reminders WHERE id = $1 AND task_id = $2 AND user_id = $3`, id, taskID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Reminders are the person's reminders on a task, the coming ones first.
func (s *Store) Reminders(ctx context.Context, userID, taskID string) ([]Reminder, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id, r.recipient, r.remind_at, coalesce(r.before_minutes, 0),
			coalesce(r.remind_at, t.deadline - make_interval(mins => r.before_minutes)), r.sent_at
		FROM task_reminders r JOIN tasks t ON t.id = r.task_id
		WHERE r.task_id = $1 AND r.user_id = $2
		ORDER BY r.sent_at IS NOT NULL, 5 NULLS LAST, r.created_at`, taskID, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Reminder, error) {
		var x Reminder
		err := r.Scan(&x.ID, &x.Recipient, &x.At, &x.BeforeMinutes, &x.Due, &x.Sent)
		return x, err
	})
}

// RemindDue sends the reminders whose time has come (go24 worker). Each is sent once; reminders of closed tasks are
// dropped unsent.
func (s *Store) RemindDue(ctx context.Context, now time.Time) (int, error) {
	sent := 0
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE task_reminders r SET sent_at = $1 FROM tasks t
			WHERE t.id = r.task_id AND r.sent_at IS NULL AND t.status = 'completed'`, now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE task_reminders r SET sent_at = $1 FROM tasks t
			WHERE t.id = r.task_id AND r.sent_at IS NULL
			  AND coalesce(r.remind_at, t.deadline - make_interval(mins => r.before_minutes)) <= $1
			RETURNING r.task_id, r.user_id,
			  CASE r.recipient WHEN 'responsible' THEN t.responsible_id WHEN 'creator' THEN t.creator_id ELSE r.user_id END`, now)
		if err != nil {
			return err
		}
		type due struct {
			task, by, to string
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
			var d due
			err := r.Scan(&d.task, &d.by, &d.to)
			return d, err
		})
		if err != nil {
			return err
		}
		for _, d := range list {
			if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, task_id, body)
				VALUES ($1, $2, 'task_remind', $3, '')`, d.to, d.by, d.task); err != nil {
				return err
			}
			sent++
		}
		return nil
	})
	return sent, err
}
