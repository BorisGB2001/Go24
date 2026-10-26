package web

import (
	"net/http"
	"slices"
	"time"

	"go24/internal/auth"
	"go24/internal/feed"
)

// thanksPeriods are the quick periods of «Благодарности»: this month, quarter, year; "" — all the time.
var thanksPeriods = []string{"month", "quarter", "year"}

// thanksPage is «Благодарности» of the company: who thanked whom and for what, newest first, and the
// most thanked people of the period («почётная доска»); ?period=month|quarter|year or ?from=&to=, ?dept= (with
// sub-departments), ?user=, ?badge=.
func (s *Server) thanksPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_, depts, err := s.tree(r)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	q := r.URL.Query()
	f := feed.BoardFilter{DepartmentID: q.Get("dept"), UserID: q.Get("user"), Badge: q.Get("badge")}
	period := q.Get("period")
	if now := time.Now(); slices.Contains(thanksPeriods, period) {
		y, m, _ := now.Date()
		switch period {
		case "month":
			f.From = time.Date(y, m, 1, 0, 0, 0, 0, time.Local)
			f.To = f.From.AddDate(0, 1, -1)
		case "quarter":
			f.From = time.Date(y, (m-1)/3*3+1, 1, 0, 0, 0, 0, time.Local)
			f.To = f.From.AddDate(0, 3, -1)
		case "year":
			f.From = time.Date(y, 1, 1, 0, 0, 0, 0, time.Local)
			f.To = f.From.AddDate(1, 0, -1)
		}
	} else {
		period = ""
		f.From, _ = time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local)
		f.To, _ = time.ParseInLocation(time.DateOnly, q.Get("to"), time.Local)
	}
	list, err := s.Feed.Board(r.Context(), sess.UserID, f)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	leaders, err := s.Feed.Leaders(r.Context(), sess.UserID, f, 10)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	var person string
	if f.UserID != "" {
		if p, err := s.Company.Profile(r.Context(), f.UserID); err == nil {
			person = p.Name
		}
	}
	s.render(w, r, "thanks", page{TitleKey: "gratitude.page", Nav: "thanks", Data: map[string]any{
		"List": list, "Leaders": leaders, "Filter": f, "Period": period, "Periods": thanksPeriods, "Person": person,
		"Departments": depts, "Badges": feed.Badges, "BadgeEmoji": feed.BadgeEmoji, "Me": sess.UserID}})
}
