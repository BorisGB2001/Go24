package tasks

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// TimeEntry is a piece of work logged on a task.
type TimeEntry struct {
	ID       string
	UserID   string
	UserName string
	Seconds  int
	SpentOn  time.Time
	Comment  string
	Source   string // manual | timer
}

// Timer is the user's running timer (at most one per user).
type Timer struct {
	TaskKey   string
	TaskTitle string
	StartedAt time.Time
}

const maxEntrySeconds = 7 * 24 * 3600

// canLogTime: the people who do the task and project managers.
func (a access) canLogTime() bool { return a.Roles.Worker() || a.Roles.Manager }

// StartTimer starts the user's timer on a task. A timer already running on another task is stopped and logged first.
func (s *Store) StartTimer(ctx context.Context, userID, taskID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, false)
		if err != nil {
			return err
		}
		if !a.canLogTime() {
			return ErrForbidden
		}
		if err := stopTimer(ctx, tx, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO task_timers (user_id, task_id) VALUES ($1, $2)`, userID, taskID)
		return err
	})
}

// StopTimer stops the user's timer and logs the elapsed time; no-op without a running timer.
func (s *Store) StopTimer(ctx context.Context, userID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error { return stopTimer(ctx, tx, userID) })
}

func stopTimer(ctx context.Context, tx pgx.Tx, userID string) error {
	var taskID string
	var started time.Time
	err := tx.QueryRow(ctx, `DELETE FROM task_timers WHERE user_id = $1 RETURNING task_id, started_at`, userID).Scan(&taskID, &started)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	secs := int(time.Since(started).Seconds())
	if secs < 1 {
		return nil
	}
	secs = min(secs, maxEntrySeconds)
	if _, err = tx.Exec(ctx, `INSERT INTO time_entries (task_id, user_id, seconds, spent_on, source) VALUES ($1, $2, $3, $4, 'timer')`,
		taskID, userID, secs, started.Local().Format("2006-01-02")); err != nil {
		return err
	}
	return addHistory(ctx, tx, taskID, userID, "time", "", TimeValue(secs, started.Local(), "⏱"))
}

// RunningTimer returns the user's running timer or nil.
func (s *Store) RunningTimer(ctx context.Context, userID string) (*Timer, error) {
	var t Timer
	err := s.db.QueryRow(ctx, `SELECT p.key || '-' || t.number, t.title, tt.started_at FROM task_timers tt
		JOIN tasks t ON t.id = tt.task_id JOIN projects p ON p.id = t.project_id WHERE tt.user_id = $1`, userID).
		Scan(&t.TaskKey, &t.TaskTitle, &t.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// AddTime logs time manually.
func (s *Store) AddTime(ctx context.Context, userID, taskID string, seconds int, spentOn time.Time, comment string) error {
	if seconds <= 0 || seconds > maxEntrySeconds {
		return invalid("err.time_bad_duration")
	}
	a, err := loadAccess(ctx, s.db, userID, taskID, false)
	if err != nil {
		return err
	}
	if !a.canLogTime() {
		return ErrForbidden
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO time_entries (task_id, user_id, seconds, spent_on, comment) VALUES ($1, $2, $3, $4, $5)`,
			taskID, userID, seconds, spentOn.Format("2006-01-02"), strings.TrimSpace(comment)); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "time", "", TimeValue(seconds, spentOn, comment))
	})
}

// UpdateTime changes an entry (duration, day, comment): its author or a project manager.
func (s *Store) UpdateTime(ctx context.Context, userID, taskID, entryID string, seconds int, spentOn time.Time, comment string) error {
	if seconds <= 0 || seconds > maxEntrySeconds {
		return invalid("err.time_bad_duration")
	}
	a, err := loadAccess(ctx, s.db, userID, taskID, false)
	if err != nil {
		return err
	}
	var owner string
	err = s.db.QueryRow(ctx, `SELECT user_id FROM time_entries WHERE id = $1 AND task_id = $2`, entryID, taskID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != userID && !a.Roles.Manager {
		return ErrForbidden
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var oldS int
		var oldDay time.Time
		var oldC string
		if err := tx.QueryRow(ctx, `SELECT seconds, spent_on, comment FROM time_entries WHERE id = $1`, entryID).Scan(&oldS, &oldDay, &oldC); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE time_entries SET seconds = $2, spent_on = $3, comment = $4 WHERE id = $1`,
			entryID, seconds, spentOn.Format("2006-01-02"), strings.TrimSpace(comment)); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "time", TimeValue(oldS, oldDay, oldC), TimeValue(seconds, spentOn, comment))
	})
}

// DeleteTime removes an entry: its author or a project manager.
func (s *Store) DeleteTime(ctx context.Context, userID, taskID, entryID string) error {
	a, err := loadAccess(ctx, s.db, userID, taskID, false)
	if err != nil {
		return err
	}
	var owner string
	err = s.db.QueryRow(ctx, `SELECT user_id FROM time_entries WHERE id = $1 AND task_id = $2`, entryID, taskID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != userID && !a.Roles.Manager {
		return ErrForbidden
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var oldS int
		var oldDay time.Time
		var oldC string
		if err := tx.QueryRow(ctx, `DELETE FROM time_entries WHERE id = $1 RETURNING seconds, spent_on, comment`, entryID).Scan(&oldS, &oldDay, &oldC); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "time", TimeValue(oldS, oldDay, oldC), "")
	})
}

// TaskTime lists a task's time entries (newest first) and the total in seconds.
func (s *Store) TaskTime(ctx context.Context, taskID string) ([]TimeEntry, int, error) {
	rows, err := s.db.Query(ctx, `SELECT e.id, e.user_id, u.name, e.seconds, e.spent_on, e.comment, e.source
		FROM time_entries e JOIN users u ON u.id = e.user_id WHERE e.task_id = $1 ORDER BY e.spent_on DESC, e.created_at DESC`, taskID)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[TimeEntry])
	total := 0
	for _, e := range list {
		total += e.Seconds
	}
	return list, total, err
}

// ParseDuration reads "1:30", "1.5", "90m", "2h", "1h 30m" into seconds.
func ParseDuration(s string) (int, error) {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), ",", "."))
	if s == "" {
		return 0, i18n.E("err.time_bad_duration")
	}
	if h, m, ok := strings.Cut(s, ":"); ok {
		hd, err1 := time.ParseDuration(h + "h")
		md, err2 := time.ParseDuration(m + "m")
		if err1 != nil || err2 != nil {
			return 0, i18n.E("err.time_bad_duration")
		}
		return int((hd + md).Seconds()), nil
	}
	s = strings.NewReplacer("ч", "h", "мин", "m", "м", "m", " ", "").Replace(s)
	if !strings.ContainsAny(s, "hm") {
		s += "h" // a bare number means hours
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, i18n.E("err.time_bad_duration")
	}
	return int(d.Seconds()), nil
}
