package company

import (
	"context"
	"fmt"
	"math"
	"strconv"
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
	IP       string
	Office   *string
	Terminal string // the time terminal it was done at, «Терминал учёта времени»
	Geo      *Geo   // where the browser said it was («Местоположение»), nil — not known
	// Device is what the day is marked from: browser, terminal or app (REST); "" — not by the person (a workflow),
	// the rules of the schedule are not checked
	Device string
}

// Geo is a position on the map: degrees and the accuracy in metres.
type Geo struct {
	Lat, Lon float64
	Acc      int
}

// ParseGeo reads a position as the browser gives it; nil when absent or out of range.
func ParseGeo(lat, lon, acc string) *Geo {
	la, err1 := strconv.ParseFloat(strings.TrimSpace(lat), 64)
	lo, err2 := strconv.ParseFloat(strings.TrimSpace(lon), 64)
	if err1 != nil || err2 != nil {
		return nil
	}
	a, _ := strconv.ParseFloat(strings.TrimSpace(acc), 64)
	return NewGeo(la, lo, a)
}

// NewGeo is a position of degrees and the accuracy in metres; nil when out of range (0, 0 — none given).
func NewGeo(lat, lon, acc float64) *Geo {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 || lat == 0 && lon == 0 {
		return nil
	}
	if acc < 0 || math.IsNaN(acc) {
		acc = 0
	}
	return &Geo{Lat: math.Round(lat*1e6) / 1e6, Lon: math.Round(lon*1e6) / 1e6, Acc: int(min(math.Round(acc), 1e6))}
}

// MapURL opens the position on OpenStreetMap.
func (g Geo) MapURL() string {
	return fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=17/%.6f/%.6f", g.Lat, g.Lon, g.Lat, g.Lon)
}

// String is the position as «55.751244, 37.618423».
func (g Geo) String() string { return fmt.Sprintf("%.6f, %.6f", g.Lat, g.Lon) }

func geoOf(lat, lon *float64, acc *int) *Geo {
	if lat == nil || lon == nil {
		return nil
	}
	g := &Geo{Lat: *lat, Lon: *lon}
	if acc != nil {
		g.Acc = *acc
	}
	return g
}

// geoArgs are a position's columns (NULL when not known).
func geoArgs(g *Geo) []any {
	if g == nil {
		return []any{nil, nil, nil}
	}
	return []any{g.Lat, g.Lon, g.Acc}
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
	w.start_ip, w.start_office, w.start_terminal, w.end_ip, w.end_office, w.end_terminal,
	w.start_lat, w.start_lon, w.start_acc, w.end_lat, w.end_lon, w.end_acc
	FROM workdays w JOIN users u ON u.id = w.user_id`

func scanWorkdays(rows pgx.Rows, err error) ([]Workday, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Workday, error) {
		var d Workday
		var sLat, sLon, eLat, eLon *float64
		var sAcc, eAcc *int
		err := r.Scan(&d.UserID, &d.Name, &d.Day, &d.Started, &d.Ended, &d.PausedAt, &d.PauseSecs, &d.Report,
			&d.Start.IP, &d.Start.Office, &d.Start.Terminal, &d.End.IP, &d.End.Office, &d.End.Terminal,
			&sLat, &sLon, &sAcc, &eLat, &eLon, &eAcc)
		d.Start.Geo, d.End.Geo = geoOf(sLat, sLon, sAcc), geoOf(eLat, eLon, eAcc)
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
	if at.Device != "" { // «Устройства для отметки» and «Отчёт о рабочем дне» of the person's schedule
		rules, err := s.DayRulesOf(ctx, userID)
		if err != nil {
			return err
		}
		if !rules.Allows(at.Device) {
			return i18n.E("err.workday_device")
		}
		if action == "finish" {
			switch rules.Report {
			case "required":
				if strings.TrimSpace(report) == "" {
					return i18n.E("err.workday_report_required")
				}
			case "none":
				report = ""
			}
		}
	}
	var sql string
	switch action {
	case "start":
		sql = `INSERT INTO workdays (user_id, day, started_at, start_ip, start_office, start_terminal, active_at,
				start_lat, start_lon, start_acc)
			VALUES ($1, $2, $3, $4, $5, $6, $3, $7, $8, $9)
			ON CONFLICT DO NOTHING`
	case "pause":
		sql = `UPDATE workdays SET paused_at = $3 WHERE user_id = $1 AND day = $2 AND ended_at IS NULL AND paused_at IS NULL`
	case "resume":
		sql = `UPDATE workdays SET pause_secs = pause_secs + extract(epoch FROM $3 - paused_at)::int, paused_at = NULL,
			active_at = $3 WHERE user_id = $1 AND day = $2 AND ended_at IS NULL AND paused_at IS NOT NULL`
	case "finish":
		if len([]rune(report)) > 5000 {
			return i18n.E("err.workday_report")
		}
		sql = `UPDATE workdays SET pause_secs = pause_secs + coalesce(extract(epoch FROM $3 - paused_at)::int, 0),
			paused_at = NULL, ended_at = $3, report = $7, end_ip = $4, end_office = $5, end_terminal = $6,
			end_lat = $8, end_lon = $9, end_acc = $10 WHERE user_id = $1 AND day = $2 AND ended_at IS NULL`
	case "reopen":
		sql = `UPDATE workdays SET pause_secs = pause_secs + extract(epoch FROM $3 - ended_at)::int, ended_at = NULL,
			end_ip = '', end_office = NULL, end_terminal = '', end_lat = NULL, end_lon = NULL, end_acc = NULL,
			active_at = $3 WHERE user_id = $1 AND day = $2 AND ended_at IS NOT NULL`
	default:
		return i18n.E("err.bad_form")
	}
	args := []any{userID, day, now}
	switch action {
	case "start":
		args = append(append(args, at.IP, at.Office, at.Terminal), geoArgs(at.Geo)...)
	case "finish":
		args = append(append(args, at.IP, at.Office, at.Terminal, strings.TrimSpace(report)), geoArgs(at.Geo)...)
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
