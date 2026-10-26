package openlines

import (
	"context"
	"sort"
	"time"
)

// StatsFilter of «Статистика»: the days From…To (both included, local time), a line and an operator ("" — all).
type StatsFilter struct {
	From, To           time.Time
	Widget, OperatorID string
}

// StatsRow is a line, an operator or a day of the report. New, Spam, NoAnswer, the first answer and the votes are of
// the dialogues started in the period; Closed and the time to close — of those closed in it.
type StatsRow struct {
	Key, Name                             string    // the line's or the operator's id ("" — no operator), the day "2006-01-02"
	Day                                   time.Time // of a day's row
	New, Closed, NoAnswer, Spam, Up, Down int
	answered, closedN                     int
	answer, close                         time.Duration
}

// FirstAnswer is the average time from the visitor's first message to the operator's first answer.
func (r StatsRow) FirstAnswer() time.Duration { return avg(r.answer, r.answered) }

// ToClose is the average time from the visitor's first message to closing.
func (r StatsRow) ToClose() time.Duration { return avg(r.close, r.closedN) }

func avg(sum time.Duration, n int) time.Duration {
	if n == 0 {
		return 0
	}
	return (sum / time.Duration(n)).Round(time.Second)
}

// Stats is the report: the totals, by lines, by operators and by days (every day of the period, also empty ones).
type Stats struct {
	Total                  StatsRow
	Lines, Operators, Days []StatsRow
}

// Stats counts the dialogues of the period.
func (s *Store) Stats(ctx context.Context, f StatsFilter) (Stats, error) {
	from := time.Date(f.From.Year(), f.From.Month(), f.From.Day(), 0, 0, 0, 0, time.Local)
	to := time.Date(f.To.Year(), f.To.Month(), f.To.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	rows, err := s.db.Query(ctx, `SELECT s.widget_id::text, w.name, coalesce(s.operator_id::text, ''), coalesce(o.name, ''),
		s.started_at, s.answered_at, s.closed_at, s.spam, s.vote
		FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users o ON o.id = s.operator_id
		WHERE s.started_at IS NOT NULL AND ((s.started_at >= $1 AND s.started_at < $2) OR (s.closed_at >= $1 AND s.closed_at < $2))
		AND ($3 = '' OR s.widget_id::text = $3) AND ($4 = '' OR s.operator_id::text = $4)`, from, to, f.Widget, f.OperatorID)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	lines, ops, days := map[string]*StatsRow{}, map[string]*StatsRow{}, map[string]*StatsRow{}
	row := func(m map[string]*StatsRow, key, name string) *StatsRow {
		r := m[key]
		if r == nil {
			r = &StatsRow{Key: key, Name: name}
			m[key] = r
		}
		return r
	}
	var st Stats
	for rows.Next() {
		var wid, wname, oid, oname string
		var started time.Time
		var answered, closed *time.Time
		var spam bool
		var vote int
		if err := rows.Scan(&wid, &wname, &oid, &oname, &started, &answered, &closed, &spam, &vote); err != nil {
			return Stats{}, err
		}
		in := func(t *time.Time) bool { return t != nil && !t.Before(from) && t.Before(to) }
		targets := []*StatsRow{&st.Total, row(lines, wid, wname), row(ops, oid, oname)}
		if in(&started) {
			for _, r := range append(targets, row(days, started.In(time.Local).Format(time.DateOnly), "")) {
				switch {
				case spam:
					r.Spam++
					continue
				case answered == nil:
					r.NoAnswer++
				case !answered.Before(started):
					r.answered++
					r.answer += answered.Sub(started)
				}
				r.New++
				switch vote {
				case 1:
					r.Up++
				case -1:
					r.Down++
				}
			}
		}
		if in(closed) && !spam {
			for _, r := range append(targets, row(days, closed.In(time.Local).Format(time.DateOnly), "")) {
				r.Closed++
				r.closedN++
				r.close += closed.Sub(started)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	sorted := func(m map[string]*StatsRow) []StatsRow {
		out := make([]StatsRow, 0, len(m))
		for _, r := range m {
			out = append(out, *r)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].New != out[j].New {
				return out[i].New > out[j].New
			}
			return out[i].Name < out[j].Name
		})
		return out
	}
	st.Lines, st.Operators = sorted(lines), sorted(ops)
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		k := d.Format(time.DateOnly)
		r := StatsRow{Key: k}
		if x := days[k]; x != nil {
			r = *x
		}
		r.Day = d
		st.Days = append(st.Days, r)
	}
	return st, nil
}
