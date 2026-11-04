package telephony

import (
	"context"
	"math"
	"strconv"
	"time"
)

// «Статистика звонков»: the calls of the external lines (with outside numbers) of a period — in total,
// by employee and by day. Calls between employees are not counted.

// StatFilter picks the calls of the statistics.
type StatFilter struct {
	From, To time.Time // started in [From, To)
	Line     string    // the external line's number ("" — all)
	UserID   string    // the employee ("" — everyone)
	Users    []string  // not nil: only the calls of these employees (the rights' «dept»)
}

// StatRow is the numbers of an employee (Who.UserID "" — calls nobody of go24's took) or of all.
type StatRow struct {
	Who
	In, Missed  int // incoming calls; those nobody answered
	Out, OutOK  int // outgoing calls; those the other end answered
	Seconds     int // talked
	WaitSeconds int // the answered incoming calls waited for the answer
	Rated       int // the conversations the clients rated
	Ratings     int // the sum of their ratings
	Scored      int // the conversations CoPilot scored by a script
	Scores      int // the sum of their scores, %
}

// AvgScore is CoPilot's average score by the scripts, % (-1 — none scored).
func (r StatRow) AvgScore() int {
	if r.Scored == 0 {
		return -1
	}
	return (r.Scores + r.Scored/2) / r.Scored
}

// AvgRating is the clients' average rating, "4.5" ("" — none rated).
func (r StatRow) AvgRating() string {
	if r.Rated == 0 {
		return ""
	}
	return strconv.FormatFloat(float64(r.Ratings)/float64(r.Rated), 'f', 1, 64)
}

// Answered is the number of incoming calls answered.
func (r StatRow) Answered() int { return r.In - r.Missed }

// MissedPct is the share of the incoming calls missed, %.
func (r StatRow) MissedPct() int {
	if r.In == 0 {
		return 0
	}
	return (r.Missed*100 + r.In/2) / r.In
}

// AvgTalk is the average conversation, s.
func (r StatRow) AvgTalk() int {
	if n := r.Answered() + r.OutOK; n > 0 {
		return (r.Seconds + n/2) / n
	}
	return 0
}

// AvgWait is how long an answered incoming call waited for the answer on average, s.
func (r StatRow) AvgWait() int {
	if n := r.Answered(); n > 0 {
		return (r.WaitSeconds + n/2) / n
	}
	return 0
}

// StatDay is the calls of a day.
type StatDay struct {
	Day             time.Time
	In, Missed, Out int
}

// Answered is the number of the day's incoming calls answered.
func (d StatDay) Answered() int { return d.In - d.Missed }

// CallStats is the statistics of the period.
type CallStats struct {
	Total  StatRow
	Prev   StatRow   // the same numbers of the period of the same length just before
	Users  []StatRow // most calls first
	Days   []StatDay // every day of the period, the days without calls too
	Hours  [7][24]StatHour
	Missed StatMissed
}

// StatHour is the incoming calls of an hour of a weekday (Monday first), «Нагрузка по времени суток».
type StatHour struct{ In, Missed int }

// StatMissed is «Пропущенные звонки»: whether the clients of the missed calls were talked to after —
// called back or got through themselves.
type StatMissed struct {
	Missed, Recalled int // the missed incoming calls; those a conversation with the number followed
	RecallSeconds    int // the sum of the times from those calls to the conversations
	Waiting          []MissedNumber
}

// waitingMax is how many numbers nobody called back the statistics lists.
const waitingMax = 50

// MissedNumber is a number whose missed calls no conversation followed yet.
type MissedNumber struct {
	Number    string
	Times     int       // its missed calls in the period
	Last      time.Time // the latest of them
	Who                 // the employee that call was for (UserID "" — nobody)
	CRMEntity string    // the client's card of that call ("" — none)
	CRMID     string
	CRMName   string
}

// CRMPath is the page of the client's card.
func (m MissedNumber) CRMPath() string {
	return Call{CRMEntity: m.CRMEntity, CRMID: m.CRMID}.CRMPath()
}

// NotRecalled is the number of the missed calls no conversation followed.
func (m StatMissed) NotRecalled() int { return m.Missed - m.Recalled }

// RecalledPct is the share of the missed calls a conversation followed, %.
func (m StatMissed) RecalledPct() int {
	if m.Missed == 0 {
		return 0
	}
	return (m.Recalled*100 + m.Missed/2) / m.Missed
}

// AvgRecall is how long after a missed call the conversation came on average, s.
func (m StatMissed) AvgRecall() int {
	if m.Recalled == 0 {
		return 0
	}
	return (m.RecallSeconds + m.Recalled/2) / m.Recalled
}

// statCalls is the SQL of the counted calls: their direction and the employee on go24's end.
const statCalls = `WITH c AS (
	SELECT CASE WHEN from_user IS NULL THEN 'in' WHEN to_user IS NULL THEN 'out' END AS dir,
		CASE WHEN from_user IS NULL THEN to_user ELSE from_user END AS emp, status, seconds, started_at, answered_at, rating, score,
		id, from_number
	FROM telephony_calls
	WHERE line <> '' AND started_at >= $1 AND started_at < $2 AND ($3 = '' OR line = $3)
	  AND ($4 = '' OR from_user::text = $4 OR to_user::text = $4)
	  AND ($5::text[] IS NULL OR from_user::text = ANY($5) OR to_user::text = ANY($5)))`

// Stats is the statistics of the calls by f; the days and hours are counted in tz.
func (s *Store) Stats(ctx context.Context, f StatFilter, tz *time.Location) (CallStats, error) {
	var st CallStats
	var err error
	if st.Users, st.Total, err = s.statUsers(ctx, f); err != nil {
		return st, err
	}
	prev := f
	prev.From, prev.To = f.From.AddDate(0, 0, -Days(f.From, f.To)), f.From
	if _, st.Prev, err = s.statUsers(ctx, prev); err != nil {
		return st, err
	}
	if st.Hours, err = s.statHours(ctx, f, tz); err != nil {
		return st, err
	}
	if st.Missed, err = s.statMissed(ctx, f); err != nil {
		return st, err
	}
	days := map[string]StatDay{}
	rows, err := s.db.Query(ctx, statCalls+`
		SELECT to_char(started_at AT TIME ZONE $6, 'YYYY-MM-DD'), count(*) FILTER (WHERE dir = 'in'),
			count(*) FILTER (WHERE dir = 'in' AND status <> 'answered'), count(*) FILTER (WHERE dir = 'out')
		FROM c WHERE dir IS NOT NULL GROUP BY 1`, f.From, f.To, f.Line, f.UserID, f.Users, tz.String())
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var d StatDay
		if err := rows.Scan(&day, &d.In, &d.Missed, &d.Out); err != nil {
			return st, err
		}
		days[day] = d
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	for d := f.From.In(tz); d.Before(f.To); d = d.AddDate(0, 0, 1) {
		day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, tz)
		row := days[day.Format(time.DateOnly)]
		row.Day = day
		st.Days = append(st.Days, row)
	}
	return st, nil
}

// Days is the number of whole days from from to to (a day of a change of the clocks counts as one).
func Days(from, to time.Time) int {
	return int(math.Round(to.Sub(from).Hours() / 24))
}

// statUsers is the numbers of the calls by f by employee and in total.
func (s *Store) statUsers(ctx context.Context, f StatFilter) ([]StatRow, StatRow, error) {
	var users []StatRow
	var total StatRow
	rows, err := s.db.Query(ctx, statCalls+`
		SELECT coalesce(c.emp::text, ''), coalesce(CASE WHEN u.name <> '' THEN u.name ELSE u.email END, ''), coalesce(u.photo_key, ''),
			count(*) FILTER (WHERE dir = 'in'), count(*) FILTER (WHERE dir = 'in' AND status <> 'answered'),
			count(*) FILTER (WHERE dir = 'out'), count(*) FILTER (WHERE dir = 'out' AND status = 'answered'),
			coalesce(sum(seconds) FILTER (WHERE status = 'answered'), 0)::int,
			coalesce(sum(extract(epoch FROM answered_at - started_at)) FILTER (WHERE dir = 'in' AND answered_at IS NOT NULL), 0)::int,
			count(*) FILTER (WHERE rating > 0), coalesce(sum(rating), 0)::int,
			count(score), coalesce(sum(score), 0)::int
		FROM c LEFT JOIN users u ON u.id = c.emp WHERE dir IS NOT NULL
		GROUP BY c.emp, u.name, u.email, u.photo_key ORDER BY count(*) DESC, 2`, f.From, f.To, f.Line, f.UserID, f.Users)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	for rows.Next() {
		var r StatRow
		if err := rows.Scan(&r.UserID, &r.Name, &r.PhotoKey, &r.In, &r.Missed, &r.Out, &r.OutOK, &r.Seconds, &r.WaitSeconds, &r.Rated, &r.Ratings, &r.Scored, &r.Scores); err != nil {
			return nil, total, err
		}
		users = append(users, r)
		total.In += r.In
		total.Missed += r.Missed
		total.Out += r.Out
		total.OutOK += r.OutOK
		total.Seconds += r.Seconds
		total.WaitSeconds += r.WaitSeconds
		total.Rated += r.Rated
		total.Ratings += r.Ratings
		total.Scored += r.Scored
		total.Scores += r.Scores
	}
	return users, total, rows.Err()
}

// statHours is the incoming calls by f by weekday and hour in tz.
func (s *Store) statHours(ctx context.Context, f StatFilter, tz *time.Location) ([7][24]StatHour, error) {
	var h [7][24]StatHour
	rows, err := s.db.Query(ctx, statCalls+`
		SELECT extract(isodow FROM started_at AT TIME ZONE $6)::int, extract(hour FROM started_at AT TIME ZONE $6)::int,
			count(*), count(*) FILTER (WHERE status <> 'answered')
		FROM c WHERE dir = 'in' GROUP BY 1, 2`, f.From, f.To, f.Line, f.UserID, f.Users, tz.String())
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var dow, hour int
		var x StatHour
		if err := rows.Scan(&dow, &hour, &x.In, &x.Missed); err != nil {
			return h, err
		}
		if dow >= 1 && dow <= 7 && hour >= 0 && hour < 24 {
			h[dow-1][hour] = x
		}
	}
	return h, rows.Err()
}

// numberKey is the SQL of a number's last 10 digits: +7…, 7…, 8… are one number (as the black list compares them).
func numberKey(col string) string {
	return `right(regexp_replace(` + col + `, '\D', '', 'g'), 10)`
}

// statMissed is «Пропущенные звонки» by f: a missed incoming call is recalled when an answered call with its number
// (outgoing or incoming, by anybody) started after it.
func (s *Store) statMissed(ctx context.Context, f StatFilter) (StatMissed, error) {
	var m StatMissed
	missed := `, m AS (
		SELECT c.id, c.emp, c.started_at, c.from_number, ` + numberKey("c.from_number") + ` AS key,
			(SELECT min(t.started_at) FROM telephony_calls t WHERE t.status = 'answered' AND t.started_at > c.started_at
			   AND ` + numberKey("CASE WHEN t.from_user IS NULL THEN t.from_number WHEN t.to_user IS NULL THEN t.to_number END") + ` = ` + numberKey("c.from_number") + `) AS back
		FROM c WHERE dir = 'in' AND status <> 'answered')`
	err := s.db.QueryRow(ctx, statCalls+missed+`
		SELECT count(*), count(back), coalesce(sum(extract(epoch FROM back - started_at)), 0)::int FROM m`,
		f.From, f.To, f.Line, f.UserID, f.Users).Scan(&m.Missed, &m.Recalled, &m.RecallSeconds)
	if err != nil || m.Recalled == m.Missed {
		return m, err
	}
	rows, err := s.db.Query(ctx, statCalls+missed+`, w AS (
		SELECT (array_agg(id ORDER BY started_at DESC))[1] AS last_id, count(*) AS times
		FROM m WHERE back IS NULL AND key <> '' GROUP BY key)
		SELECT c.from_number, w.times, c.started_at, coalesce(c.to_user::text, ''),
			coalesce(CASE WHEN u.name <> '' THEN u.name ELSE u.email END, ''), coalesce(u.photo_key, ''), `+callCRM+`
		FROM w JOIN telephony_calls c ON c.id = w.last_id LEFT JOIN users u ON u.id = c.to_user`+callCRMJoin+`
		ORDER BY c.started_at DESC LIMIT $6`, f.From, f.To, f.Line, f.UserID, f.Users, waitingMax)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var n MissedNumber
		if err := rows.Scan(&n.Number, &n.Times, &n.Last, &n.UserID, &n.Name, &n.PhotoKey, &n.CRMEntity, &n.CRMID, &n.CRMName); err != nil {
			return m, err
		}
		m.Waiting = append(m.Waiting, n)
	}
	return m, rows.Err()
}
