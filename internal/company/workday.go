package company

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Рабочее время»: the working day of an employee — started, paused, finished with a report.

// Workday is one day of a person.
type Workday struct {
	UserID, Name string
	Day          time.Time
	Started      time.Time
	Ended        *time.Time
	PausedAt     *time.Time
	PauseSecs    int
	Report       string
	Start, End   Place // where the day was started and finished
}

// Place is where a working day was started or finished: the address and the office network (nil — no office
// networks were set; "" — outside them).
type Place struct {
	IP     string
	Office *string
}

// Away: the place is outside the office networks.
func (p Place) Away() bool { return p.Office != nil && *p.Office == "" }

// OfficeName is the office network the place is in, "" — outside or not tracked.
func (p Place) OfficeName() string {
	if p.Office == nil {
		return ""
	}
	return *p.Office
}

// State: running | paused | done.
func (d Workday) State() string {
	switch {
	case d.Ended != nil:
		return "done"
	case d.PausedAt != nil:
		return "paused"
	}
	return "running"
}

// Worked is the time worked until now (or the end), without pauses, in seconds.
func (d Workday) Worked(now time.Time) int {
	end := now
	if d.Ended != nil {
		end = *d.Ended
	} else if d.PausedAt != nil {
		end = *d.PausedAt
	}
	secs := int(end.Sub(d.Started).Seconds()) - d.PauseSecs
	return max(secs, 0)
}

// Late: the day started after the start of the working day (hh:mm of the company schedule).
func (d Workday) Late(start string) bool {
	t, err := time.Parse("15:04", start)
	if err != nil {
		return false
	}
	s := d.Started.Local()
	return s.Hour()*60+s.Minute() > t.Hour()*60+t.Minute()
}

const workdaySQL = `SELECT w.user_id, u.name, w.day, w.started_at, w.ended_at, w.paused_at, w.pause_secs, w.report,
	w.start_ip, w.start_office, w.end_ip, w.end_office
	FROM workdays w JOIN users u ON u.id = w.user_id`

func scanWorkdays(rows pgx.Rows, err error) ([]Workday, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Workday, error) {
		var d Workday
		err := r.Scan(&d.UserID, &d.Name, &d.Day, &d.Started, &d.Ended, &d.PausedAt, &d.PauseSecs, &d.Report,
			&d.Start.IP, &d.Start.Office, &d.End.IP, &d.End.Office)
		return d, err
	})
}

// Today is the person's working day of today (nil: not started).
func (s *Store) Today(ctx context.Context, userID string, now time.Time) (*Workday, error) {
	list, err := scanWorkdays(s.db.Query(ctx, workdaySQL+` WHERE w.user_id = $1 AND w.day = $2`, userID, now.Format(time.DateOnly)))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// WorkAction changes today's working day: start, pause, resume, finish (with the report), reopen (continue a
// finished day, as «Продолжить рабочий день»); at is where it is done — start and finish remember it.
func (s *Store) WorkAction(ctx context.Context, userID, action, report string, now time.Time, at Place) error {
	day := now.Format(time.DateOnly)
	var sql string
	switch action {
	case "start":
		sql = `INSERT INTO workdays (user_id, day, started_at, start_ip, start_office) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING`
	case "pause":
		sql = `UPDATE workdays SET paused_at = $3 WHERE user_id = $1 AND day = $2 AND ended_at IS NULL AND paused_at IS NULL`
	case "resume":
		sql = `UPDATE workdays SET pause_secs = pause_secs + extract(epoch FROM $3 - paused_at)::int, paused_at = NULL
			WHERE user_id = $1 AND day = $2 AND ended_at IS NULL AND paused_at IS NOT NULL`
	case "finish":
		if len([]rune(report)) > 5000 {
			return i18n.E("err.workday_report")
		}
		sql = `UPDATE workdays SET pause_secs = pause_secs + coalesce(extract(epoch FROM $3 - paused_at)::int, 0),
			paused_at = NULL, ended_at = $3, report = $6, end_ip = $4, end_office = $5
			WHERE user_id = $1 AND day = $2 AND ended_at IS NULL`
	case "reopen":
		sql = `UPDATE workdays SET pause_secs = pause_secs + extract(epoch FROM $3 - ended_at)::int, ended_at = NULL,
			end_ip = '', end_office = NULL WHERE user_id = $1 AND day = $2 AND ended_at IS NOT NULL`
	default:
		return i18n.E("err.bad_form")
	}
	args := []any{userID, day, now}
	switch action {
	case "start":
		args = append(args, at.IP, at.Office)
	case "finish":
		args = append(args, at.IP, at.Office, strings.TrimSpace(report))
	}
	tag, err := s.db.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return i18n.E("err.workday_state")
	}
	return nil
}

// Workdays are the person's days in [from, to).
func (s *Store) Workdays(ctx context.Context, userID string, from, to time.Time) ([]Workday, error) {
	return scanWorkdays(s.db.Query(ctx, workdaySQL+` WHERE w.user_id = $1 AND w.day >= $2 AND w.day < $3 ORDER BY w.day DESC`,
		userID, from.Format(time.DateOnly), to.Format(time.DateOnly)))
}

// TeamDay is the day of the people (a manager's subordinates); those who have not started are not listed.
func (s *Store) TeamDay(ctx context.Context, people []string, day time.Time) ([]Workday, error) {
	if len(people) == 0 {
		return nil, nil
	}
	return scanWorkdays(s.db.Query(ctx, workdaySQL+` WHERE w.user_id::text = ANY($1) AND w.day = $2 ORDER BY u.name`,
		people, day.Format(time.DateOnly)))
}

// ActiveIDs are the ids of all active employees.
func (s *Store) ActiveIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text FROM users WHERE active ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
