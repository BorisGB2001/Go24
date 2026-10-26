package telephony

import (
	"context"
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
	Total StatRow
	Users []StatRow // most calls first
	Days  []StatDay // every day of the period, the days without calls too
}

// statCalls is the SQL of the counted calls: their direction and the employee on go24's end.
const statCalls = `WITH c AS (
	SELECT CASE WHEN from_user IS NULL THEN 'in' WHEN to_user IS NULL THEN 'out' END AS dir,
		CASE WHEN from_user IS NULL THEN to_user ELSE from_user END AS emp, status, seconds, started_at, answered_at, rating
	FROM telephony_calls
	WHERE line <> '' AND started_at >= $1 AND started_at < $2 AND ($3 = '' OR line = $3)
	  AND ($4 = '' OR from_user::text = $4 OR to_user::text = $4)
	  AND ($5::text[] IS NULL OR from_user::text = ANY($5) OR to_user::text = ANY($5)))`

// Stats is the statistics of the calls by f; the days are counted in tz.
func (s *Store) Stats(ctx context.Context, f StatFilter, tz *time.Location) (CallStats, error) {
	var st CallStats
	rows, err := s.db.Query(ctx, statCalls+`
		SELECT coalesce(c.emp::text, ''), coalesce(CASE WHEN u.name <> '' THEN u.name ELSE u.email END, ''), coalesce(u.photo_key, ''),
			count(*) FILTER (WHERE dir = 'in'), count(*) FILTER (WHERE dir = 'in' AND status <> 'answered'),
			count(*) FILTER (WHERE dir = 'out'), count(*) FILTER (WHERE dir = 'out' AND status = 'answered'),
			coalesce(sum(seconds) FILTER (WHERE status = 'answered'), 0)::int,
			coalesce(sum(extract(epoch FROM answered_at - started_at)) FILTER (WHERE dir = 'in' AND answered_at IS NOT NULL), 0)::int,
			count(*) FILTER (WHERE rating > 0), coalesce(sum(rating), 0)::int
		FROM c LEFT JOIN users u ON u.id = c.emp WHERE dir IS NOT NULL
		GROUP BY c.emp, u.name, u.email, u.photo_key ORDER BY count(*) DESC, 2`, f.From, f.To, f.Line, f.UserID, f.Users)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var r StatRow
		if err := rows.Scan(&r.UserID, &r.Name, &r.PhotoKey, &r.In, &r.Missed, &r.Out, &r.OutOK, &r.Seconds, &r.WaitSeconds, &r.Rated, &r.Ratings); err != nil {
			return st, err
		}
		st.Users = append(st.Users, r)
		st.Total.In += r.In
		st.Total.Missed += r.Missed
		st.Total.Out += r.Out
		st.Total.OutOK += r.OutOK
		st.Total.Seconds += r.Seconds
		st.Total.WaitSeconds += r.WaitSeconds
		st.Total.Rated += r.Rated
		st.Total.Ratings += r.Ratings
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	days := map[string]StatDay{}
	rows, err = s.db.Query(ctx, statCalls+`
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
