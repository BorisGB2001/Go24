package web

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/telephony"
)

// phoneStatsDays is the longest period of the statistics.
const phoneStatsDays = 366

// phoneStatRow is an employee's line of the statistics with the link to their calls.
type phoneStatRow struct {
	telephony.StatRow
	Photo, Calls string
}

// phoneStats is «Статистика звонков»: the calls of the external lines of the period (this month by default) — in
// total, by day and by employee. An employee sees their own, administrators — everyone's (or one's: user=).
func (s *Server) phoneStats(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/my")
		return
	}
	perms := s.telPerms(r.Context(), sess)
	if !perms.Has("calls", "own") {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/my")
		return
	}
	q := r.URL.Query()
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if d, err := time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local); err == nil {
		from = d
	}
	if d, err := time.ParseInLocation(time.DateOnly, q.Get("to"), time.Local); err == nil {
		to = d
	}
	if to.Before(from) {
		from, to = to, from
	}
	if to.Sub(from) > phoneStatsDays*24*time.Hour {
		from = to.AddDate(0, 0, -phoneStatsDays+1)
	}
	f := telephony.StatFilter{From: from, To: to.AddDate(0, 0, 1), Line: q.Get("line")}
	var err error
	if f.UserID, f.Users, err = s.telScope(r.Context(), sess, perms["calls"], q.Get("user")); err != nil {
		s.fail(w, "telephony team", err)
		return
	}
	st, err := s.Telephony.Stats(r.Context(), f, time.Local)
	if err != nil {
		s.fail(w, "telephony stats", err)
		return
	}
	trunks, err := s.Telephony.Trunks(r.Context())
	if err != nil {
		s.fail(w, "telephony trunks", err)
		return
	}
	period := url.Values{"from": {from.Format(time.DateOnly)}, "to": {to.Format(time.DateOnly)}}
	rows := make([]phoneStatRow, len(st.Users))
	for i, u := range st.Users {
		rows[i] = phoneStatRow{StatRow: u}
		if u.UserID != "" {
			rows[i].Photo = company.PhotoURL(u.UserID, u.PhotoKey)
			v := url.Values{"user": {u.UserID}, "from": period["from"], "to": period["to"]}
			rows[i].Calls = "/telephony/calls?" + v.Encode()
		}
	}
	top := 1 // the day with the most calls is the chart's full height
	for _, d := range st.Days {
		top = max(top, d.In+d.Out)
	}
	hours := phoneHeat(st.Hours)
	waiting := make([]phoneWaiting, len(st.Missed.Waiting))
	for i, n := range st.Missed.Waiting {
		waiting[i] = phoneWaiting{MissedNumber: n}
		if n.UserID != "" {
			waiting[i].Photo = company.PhotoURL(n.UserID, n.PhotoKey)
		}
		v := url.Values{"q": {n.Number}, "from": period["from"], "to": period["to"]}
		waiting[i].Calls = "/telephony/calls?" + v.Encode()
	}
	missed := url.Values{"kind": {"missed"}, "from": period["from"], "to": period["to"]}
	if f.UserID != "" {
		missed.Set("user", f.UserID)
	}
	data := map[string]any{"Stats": st, "Rows": rows, "Top": top, "Hours": hours, "Waiting": waiting,
		"Delta": phoneDeltas(st.Total, st.Prev), "PrevFrom": from.AddDate(0, 0, -telephony.Days(from, to)-1), "PrevTo": from.AddDate(0, 0, -1), "From": period.Get("from"), "To": period.Get("to"),
		"Line": f.Line, "UserID": f.UserID, "Trunks": trunks, "Monitor": perms.Has("monitor", "dept"),
		"Calls": "/telephony/calls?" + period.Encode(), "Missed": "/telephony/calls?" + missed.Encode()}
	if perms["calls"] != "own" {
		if data["Users"], err = s.telUsers(r.Context(), f.Users); err != nil {
			s.fail(w, "telephony users", err)
			return
		}
	}
	s.render(w, r, "phonestats", page{TitleKey: "tel.stats", Nav: "telephony_calls", Data: data})
}

// phoneWaiting is a number nobody called back with the links to its calls and the employee's photo.
type phoneWaiting struct {
	telephony.MissedNumber
	Photo, Calls string
}

// phoneHeatDay is a weekday of «Нагрузка по времени суток»: its name key and the 24 hours.
type phoneHeatDay struct {
	Day   string // weekday.N
	Hours [24]phoneHeatHour
}

// phoneHeatHour is an hour's incoming calls and its shade, 0 (none) to 4 (the busiest hour).
type phoneHeatHour struct {
	telephony.StatHour
	Hour, Level int
}

// phoneHeat is the incoming calls by weekday (Monday first) and hour, shaded against the busiest hour.
func phoneHeat(h [7][24]telephony.StatHour) []phoneHeatDay {
	top := 0
	for _, d := range h {
		for _, x := range d {
			top = max(top, x.In)
		}
	}
	if top == 0 {
		return nil
	}
	days := make([]phoneHeatDay, 7)
	for i, d := range h {
		days[i].Day = fmt.Sprintf("weekday.%d", (i+1)%7)
		for hour, x := range d {
			c := phoneHeatHour{StatHour: x, Hour: hour}
			if x.In > 0 {
				c.Level = (x.In*4 + top - 1) / top
			}
			days[i].Hours[hour] = c
		}
	}
	return days
}

// phoneDelta is a number's change against the period before: Text "+12%" ("" — nothing to compare with), Up — it grew.
type phoneDelta struct {
	Text string
	Up   bool
}

// phoneDeltas is the changes of the statistics' totals against the period before.
func phoneDeltas(cur, prev telephony.StatRow) map[string]phoneDelta {
	delta := func(c, p int) phoneDelta {
		if p == 0 || c == p {
			return phoneDelta{}
		}
		d := (c - p) * 100 / p
		if d == 0 {
			return phoneDelta{}
		}
		if d > 0 {
			return phoneDelta{Text: fmt.Sprintf("+%d%%", d), Up: true}
		}
		return phoneDelta{Text: fmt.Sprintf("−%d%%", -d)}
	}
	return map[string]phoneDelta{"In": delta(cur.In, prev.In), "Answered": delta(cur.Answered(), prev.Answered()),
		"Missed": delta(cur.Missed, prev.Missed), "Out": delta(cur.Out, prev.Out), "Seconds": delta(cur.Seconds, prev.Seconds),
		"AvgTalk": delta(cur.AvgTalk(), prev.AvgTalk()), "AvgWait": delta(cur.AvgWait(), prev.AvgWait())}
}

// talkTime is seconds as m:ss (h:mm:ss for a long time).
func talkTime(sec int) string {
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}
