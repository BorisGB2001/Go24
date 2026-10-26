package company

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Доска почёта»: an administrator or an HR officer with the right puts an employee on the company's honor
// board — for what and for which days (from a day, until a day or until taken off). The board shows those on it now
// (and those to come), the archive — whose days are over; the employee is notified.

// Honor is an employee's place on the board.
type Honor struct {
	ID          int64
	Person      Person
	Departments []string
	Reason      string
	From        time.Time
	To          *time.Time // nil — until taken off
	ByID        string
	ByName      string
	Created     time.Time
}

// Coming: the place begins after the day.
func (h Honor) Coming(day time.Time) bool { return h.From.After(day) }

// HonorFilter narrows the board.
type HonorFilter struct {
	DepartmentID string // with its sub-departments
	UserID       string
	Archive      bool // the places whose days are over, instead of the current and coming ones
}

// HonorReasonMax limits the reason, in characters.
const HonorReasonMax = 500

// CanHonor: administrators and HR officers an administrator let keep the honor board.
func (s *Store) CanHonor(ctx context.Context, userID string, admin bool) (bool, error) {
	if admin {
		return true, nil
	}
	h, err := s.HROf(ctx, userID)
	return h.Can("honor"), err
}

const honorSQL = `SELECT h.id, u.id::text, u.name, u.position, u.photo_key,
		ARRAY(SELECT d.name FROM user_departments ud JOIN departments d ON d.id = ud.department_id WHERE ud.user_id = u.id ORDER BY d.name),
		h.reason, h.from_day, h.to_day, coalesce(h.created_by::text, ''), coalesce(b.name, ''), h.created_at
	FROM honors h JOIN users u ON u.id = h.user_id LEFT JOIN users b ON b.id = h.created_by`

func scanHonors(rows pgx.Rows, err error) ([]Honor, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Honor, error) {
		var h Honor
		err := row.Scan(&h.ID, &h.Person.ID, &h.Person.Name, &h.Person.Position, &h.Person.PhotoKey, &h.Departments,
			&h.Reason, &h.From, &h.To, &h.ByID, &h.ByName, &h.Created)
		h.From = localDay(h.From) // a date comes as UTC midnight: the days are compared with local ones
		if h.To != nil {
			to := localDay(*h.To)
			h.To = &to
		}
		return h, err
	})
}

func localDay(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local) }

// Honors lists the board on the day: active employees whose place has not ended — the current ones first, then those
// to come, by the day they came on; the archive — latest ended first.
func (s *Store) Honors(ctx context.Context, day time.Time, f HonorFilter) ([]Honor, error) {
	args := []any{day.Format(time.DateOnly)}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := []string{"u.active"}
	order := " ORDER BY h.from_day > $1::date, h.from_day DESC, u.name"
	if f.Archive {
		where = append(where, "h.to_day < $1::date")
		order = " ORDER BY h.to_day DESC, u.name"
	} else {
		where = append(where, "(h.to_day IS NULL OR h.to_day >= $1::date)")
	}
	if f.UserID != "" {
		where = append(where, "u.id::text = "+arg(f.UserID))
	}
	if f.DepartmentID != "" {
		where = append(where, `h.user_id IN (SELECT ud.user_id FROM user_departments ud WHERE ud.department_id IN (
			WITH RECURSIVE sub AS (SELECT id FROM departments WHERE id::text = `+arg(f.DepartmentID)+`
				UNION ALL SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id) SELECT id FROM sub))`)
	}
	return scanHonors(s.db.Query(ctx, honorSQL+" WHERE "+strings.Join(where, " AND ")+order+" LIMIT 500", args...))
}

// Honor returns a place on the board.
func (s *Store) Honor(ctx context.Context, id int64) (Honor, error) {
	list, err := scanHonors(s.db.Query(ctx, honorSQL+" WHERE h.id = $1", id))
	if err != nil {
		return Honor{}, err
	}
	if len(list) == 0 {
		return Honor{}, ErrNotFound
	}
	return list[0], nil
}

// AddHonor puts an active employee (not a guest) on the board from the day to the day (nil — until taken off) and
// tells them; who may — the caller checks (CanHonor).
func (s *Store) AddHonor(ctx context.Context, actor, userID, reason string, from time.Time, to *time.Time) (int64, error) {
	reason = strings.TrimSpace(reason)
	if from.IsZero() || (to != nil && to.Before(from)) {
		return 0, i18n.E("err.honor_dates")
	}
	if utf8.RuneCountInString(reason) > HonorReasonMax {
		return 0, i18n.E("err.bad_form")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active AND NOT guest AND oidc_sub <> 'legacy:0')`,
		userID).Scan(&ok); err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNotFound
	}
	var till any
	if to != nil {
		till = to.Format(time.DateOnly)
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO honors (user_id, reason, from_day, to_day, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		userID, reason, from.Format(time.DateOnly), till, actor).Scan(&id); err != nil {
		return 0, err
	}
	if userID != actor {
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'honor', $3, '/company/honor')`,
			userID, actor, reason); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit(ctx)
}

// DeleteHonor takes the place off the board (the archive forgets it too).
func (s *Store) DeleteHonor(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM honors WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
