package openlines

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"
)

// StatsFilter of «Статистика»: the days From…To (both included, local time), a line and an operator ("" — all).
type StatsFilter struct {
	From, To           time.Time
	Widget, OperatorID string
}

// StatsRow is a line, an operator or a day of the report. New, Spam, NoAnswer, the first answer and the votes are of
// the dialogues started in the period (KPI — of them, those that waited longer than the line's KPI); Closed and the time to close — of those closed in it.
type StatsRow struct {
	Key, Name                             string    // the line's or the operator's id ("" — no operator), the day "2006-01-02"
	Day                                   time.Time // of a day's row
	New, Closed, NoAnswer, Spam, Up, Down int
	KPI                                   int
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
		s.started_at, s.answered_at, s.closed_at, s.spam, s.vote, s.kpi_late > 0
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
		var spam, kpi bool
		var vote int
		if err := rows.Scan(&wid, &wname, &oid, &oname, &started, &answered, &closed, &spam, &vote, &kpi); err != nil {
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
				if kpi {
					r.KPI++
				}
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

// DetailFilter of «Детальная статистика»: the dialogues started in the period, of a line and an operator, of a status
// (open, closed, noanswer, spam), a channel and the client's rating (up, down, none); "" — any. Sort is one of
// DetailSorts, "-" before it — descending; "" — the newest first.
type DetailFilter struct {
	StatsFilter
	Status, Channel, Vote, Sort string
}

// DetailSorts are the columns «Детальная статистика» sorts by.
var DetailSorts = []string{"num", "started", "answer", "close", "messages"}

// DetailLimit is how many dialogues «Детальная статистика» shows at most.
const DetailLimit = 1000

// DetailRow is a dialogue of «Детальная статистика»: its line (the one the client wrote to), channel, client, operator,
// times, how many messages the client and the operators wrote, the ratings and the CRM cards.
type DetailRow struct {
	ID                                string
	Num                               int64
	Line, Channel, Name, Phone, Email string
	OperatorID, Operator, Status      string
	Started                           time.Time
	Answered, Closed                  *time.Time
	Messages, Vote, HeadVote, KPILate int
	Spam                              bool
	LeadID, ContactID                 string
}

// Client is how the dialogue's client is shown: the name or «#…».
func (r DetailRow) Client() string { return Session{ID: r.ID, Name: r.Name}.Title() }

// FirstAnswer is the time from the client's first message to the operator's first answer (0 — no answer yet).
func (r DetailRow) FirstAnswer() time.Duration {
	if r.Answered == nil || r.Answered.Before(r.Started) {
		return 0
	}
	return r.Answered.Sub(r.Started).Round(time.Second)
}

// ToClose is the time from the client's first message to closing (0 — open).
func (r DetailRow) ToClose() time.Duration {
	if r.Closed == nil || r.Closed.Before(r.Started) {
		return 0
	}
	return r.Closed.Sub(r.Started).Round(time.Second)
}

// Detail lists the dialogues of «Детальная статистика», at most DetailLimit; more tells there are others.
func (s *Store) Detail(ctx context.Context, f DetailFilter) (list []DetailRow, more bool, err error) {
	from := time.Date(f.From.Year(), f.From.Month(), f.From.Day(), 0, 0, 0, 0, time.Local)
	to := time.Date(f.To.Year(), f.To.Month(), f.To.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	order := "s.started_at DESC"
	if k := strings.TrimPrefix(f.Sort, "-"); slices.Contains(DetailSorts, k) {
		dir := " ASC"
		if strings.HasPrefix(f.Sort, "-") {
			dir = " DESC"
		}
		order = map[string]string{"num": "s.num", "started": "s.started_at", "answer": "s.answered_at - s.started_at",
			"close": "s.closed_at - s.started_at", "messages": "msgs"}[k] + dir + " NULLS LAST"
	}
	rows, err := s.db.Query(ctx, `SELECT s.id::text, s.num, w.name, s.channel, s.name, s.phone, s.email,
		coalesce(s.operator_id::text, ''), coalesce(o.name, ''), s.status, s.started_at, s.answered_at, s.closed_at,
		(SELECT count(*) FROM ol_messages m WHERE m.session_id = s.id AND m.kind = '')::int AS msgs,
		s.vote, s.head_vote, s.kpi_late, s.spam, coalesce(s.lead_id::text, ''), coalesce(s.contact_id::text, '')
		FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users o ON o.id = s.operator_id
		WHERE s.started_at >= $1 AND s.started_at < $2
		AND ($3 = '' OR s.widget_id::text = $3) AND ($4 = '' OR s.operator_id::text = $4)
		AND CASE $5 WHEN 'open' THEN s.status = 'open' AND NOT s.spam WHEN 'closed' THEN s.status = 'closed' AND NOT s.spam
			WHEN 'noanswer' THEN s.answered_at IS NULL AND NOT s.spam WHEN 'spam' THEN s.spam ELSE true END
		AND ($6 = '' OR s.channel = $6)
		AND CASE $7 WHEN 'up' THEN s.vote > 0 WHEN 'down' THEN s.vote < 0 WHEN 'none' THEN s.vote = 0 ELSE true END
		ORDER BY `+order+`, s.num DESC LIMIT $8`,
		from, to, f.Widget, f.OperatorID, f.Status, f.Channel, f.Vote, DetailLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var x DetailRow
		if err := rows.Scan(&x.ID, &x.Num, &x.Line, &x.Channel, &x.Name, &x.Phone, &x.Email, &x.OperatorID, &x.Operator, &x.Status,
			&x.Started, &x.Answered, &x.Closed, &x.Messages, &x.Vote, &x.HeadVote, &x.KPILate, &x.Spam, &x.LeadID, &x.ContactID); err != nil {
			return nil, false, err
		}
		list = append(list, x)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(list) > DetailLimit {
		return list[:DetailLimit], true, nil
	}
	return list, false, nil
}
