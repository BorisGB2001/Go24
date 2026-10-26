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
	missed := url.Values{"kind": {"missed"}, "from": period["from"], "to": period["to"]}
	if f.UserID != "" {
		missed.Set("user", f.UserID)
	}
	data := map[string]any{"Stats": st, "Rows": rows, "Top": top, "From": period.Get("from"), "To": period.Get("to"),
		"Line": f.Line, "UserID": f.UserID, "Trunks": trunks,
		"Calls": "/telephony/calls?" + period.Encode(), "Missed": "/telephony/calls?" + missed.Encode()}
	if perms["calls"] != "own" {
		if data["Users"], err = s.telUsers(r.Context(), f.Users); err != nil {
			s.fail(w, "telephony users", err)
			return
		}
	}
	s.render(w, r, "phonestats", page{TitleKey: "tel.stats", Nav: "telephony_calls", Data: data})
}

// talkTime is seconds as m:ss (h:mm:ss for a long time).
func talkTime(sec int) string {
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}
