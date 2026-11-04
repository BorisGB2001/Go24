package company

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Контроль времени», as timeman.timecontrol: while a working day runs, the pages of go24 report that the
// person is active; a gap of the set minutes and more is an absence from the computer. The person tells where they
// were — on business or personal — and the managers see the absences of their people in a report.

// Idle is an absence during a working day.
type Idle struct {
	ID         int64
	UserID     string
	Name       string
	Day        time.Time
	Started    time.Time
	Ended      time.Time
	Kind       string // "" — not answered, work, personal
	Reason     string
	AnsweredAt *time.Time
}

// Secs is how long the absence lasted.
func (i Idle) Secs() int { return int(i.Ended.Sub(i.Started).Seconds()) }

// Alive marks the person active now. During a running day (not paused or finished) the gap since the last activity
// of at least after is an absence, which is kept and returned; nothing is counted before since (when the control was
// switched on) or before the first activity of the day.
func (s *Store) Alive(ctx context.Context, userID string, now time.Time, after time.Duration, since *time.Time) (*Idle, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	day := now.Format(time.DateOnly)
	var last *time.Time
	err = tx.QueryRow(ctx, `SELECT active_at FROM workdays WHERE user_id = $1 AND day = $2 AND ended_at IS NULL
		AND paused_at IS NULL FOR UPDATE`, userID, day).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE workdays SET active_at = $3 WHERE user_id = $1 AND day = $2`, userID, day, now); err != nil {
		return nil, err
	}
	var idle *Idle
	if last != nil && (since == nil || !last.Before(*since)) && now.Sub(*last) >= after {
		idle = &Idle{UserID: userID, Started: *last, Ended: now}
		if err := tx.QueryRow(ctx, `INSERT INTO worktime_idle (user_id, day, started_at, ended_at) VALUES ($1, $2, $3, $4)
			RETURNING id, day`, userID, day, *last, now).Scan(&idle.ID, &idle.Day); err != nil {
			return nil, err
		}
	}
	return idle, tx.Commit(ctx)
}

// AnswerIdle: the person tells where they were during their absence — work or personal, with a comment.
func (s *Store) AnswerIdle(ctx context.Context, userID string, id int64, kind, reason string, now time.Time) error {
	reason = strings.TrimSpace(reason)
	if (kind != "work" && kind != "personal") || len([]rune(reason)) > 1000 {
		return i18n.E("err.bad_form")
	}
	tag, err := s.db.Exec(ctx, `UPDATE worktime_idle SET kind = $3, reason = $4, answered_at = $5 WHERE id = $1 AND user_id = $2`,
		id, userID, kind, reason, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return i18n.E("err.not_found")
	}
	return nil
}

const idleSQL = `SELECT i.id, i.user_id, u.name, i.day, i.started_at, i.ended_at, i.kind, i.reason, i.answered_at
	FROM worktime_idle i JOIN users u ON u.id = i.user_id`

func scanIdles(rows pgx.Rows, err error) ([]Idle, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Idle, error) {
		var i Idle
		err := r.Scan(&i.ID, &i.UserID, &i.Name, &i.Day, &i.Started, &i.Ended, &i.Kind, &i.Reason, &i.AnsweredAt)
		return i, err
	})
}

// Idles are the absences of the people on the days [from, to), by time.
func (s *Store) Idles(ctx context.Context, people []string, from, to time.Time) ([]Idle, error) {
	if len(people) == 0 {
		return nil, nil
	}
	return scanIdles(s.db.Query(ctx, idleSQL+` WHERE i.user_id::text = ANY($1) AND i.day >= $2 AND i.day < $3
		ORDER BY i.started_at`, people, from.Format(time.DateOnly), to.Format(time.DateOnly)))
}

// OpenIdle is the person's last absence of today not answered yet (nil — none).
func (s *Store) OpenIdle(ctx context.Context, userID string, now time.Time) (*Idle, error) {
	list, err := scanIdles(s.db.Query(ctx, idleSQL+` WHERE i.user_id = $1 AND i.day = $2 AND i.kind = ''
		ORDER BY i.started_at DESC LIMIT 1`, userID, now.Format(time.DateOnly)))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// IdleRow is a person in the report of «Контроль времени»: the absences of the period and their time — on business,
// personal and not answered.
type IdleRow struct {
	Person
	Idles                    []Idle
	Secs, Work, Personal, No int
}

// IdleReport groups the absences by the people, in the order of the people.
func IdleReport(people []Person, idles []Idle) []IdleRow {
	rows := make([]IdleRow, len(people))
	at := make(map[string]int, len(people))
	for i, p := range people {
		rows[i].Person = p
		at[p.ID] = i
	}
	for _, d := range idles {
		i, ok := at[d.UserID]
		if !ok {
			continue
		}
		r := &rows[i]
		r.Idles = append(r.Idles, d)
		r.Secs += d.Secs()
		switch d.Kind {
		case "work":
			r.Work += d.Secs()
		case "personal":
			r.Personal += d.Secs()
		default:
			r.No += d.Secs()
		}
	}
	return rows
}
