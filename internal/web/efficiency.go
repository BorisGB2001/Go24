package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// Efficiency page (Задачи → Эффективность): the person's percent for a month or a chosen period,
// a chart by days, the violations with links to the tasks; a department head also sees the people of the departments
// they head (everyone in admin mode) and opens anyone of them.

func (s *Server) efficiencyPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	now := time.Now()
	from, to := monthOf(now)
	if v, err := parseDate(r.FormValue("from")); err == nil && v != nil {
		from = time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.Local)
	}
	if v, err := parseDate(r.FormValue("to")); err == nil && v != nil {
		to = time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1) // the last day is included
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		from, to = monthOf(now)
	}

	// the team: the people of the departments the user heads; everyone in admin mode
	var team []string
	if tasks.IsAdmin(ctx) {
		users, err := s.Tasks.ListUsers(ctx)
		if err != nil {
			s.fail(w, "efficiency", err)
			return
		}
		for _, u := range users {
			team = append(team, u.ID)
		}
	} else {
		subs, err := s.Company.Subordinates(ctx, sess.UserID)
		if err != nil {
			s.fail(w, "efficiency", err)
			return
		}
		for _, p := range subs {
			team = append(team, p.ID)
		}
	}
	who := sess.UserID
	if u := r.FormValue("user"); u != "" && u != who {
		allowed := false
		for _, id := range team {
			allowed = allowed || id == u
		}
		if !allowed {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
		who = u
	}
	e, err := s.Tasks.EfficiencyOf(ctx, who, from, to, now)
	if err != nil {
		s.fail(w, "efficiency", err)
		return
	}
	var rows []tasks.Efficiency
	for _, id := range team {
		te, err := s.Tasks.EfficiencyOf(ctx, id, from, to, now)
		if err != nil {
			s.fail(w, "efficiency", err)
			return
		}
		if te.Total > 0 || !tasks.IsAdmin(ctx) { // in admin mode only those who had tasks
			rows = append(rows, te)
		}
	}
	s.render(w, r, "efficiency", page{TitleKey: "eff.title", Nav: "efficiency", Data: map[string]any{
		"E": e, "Mine": who == sess.UserID, "Chart": effChart(e.Days), "Team": rows,
		"From": from.Format("2006-01-02"), "To": to.AddDate(0, 0, -1).Format("2006-01-02"),
		"Prev": monthLink(from.AddDate(0, -1, 0), who, sess.UserID), "Next": monthLink(from.AddDate(0, 1, 0), who, sess.UserID),
		"Query": userQuery(who, sess.UserID)}})
}

// monthOf is the calendar month of t: [first day, first day of the next month).
func monthOf(t time.Time) (time.Time, time.Time) {
	from := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.Local)
	return from, from.AddDate(0, 1, 0)
}

func userQuery(who, me string) string {
	if who == me {
		return ""
	}
	return "&user=" + who
}

func monthLink(t time.Time, who, me string) string {
	from, to := monthOf(t)
	return fmt.Sprintf("/tasks/efficiency?from=%s&to=%s%s", from.Format("2006-01-02"), to.AddDate(0, 0, -1).Format("2006-01-02"), userQuery(who, me))
}

// effChart draws the percent by days (0–100 %) with the burndown chart's geometry.
func effChart(days []tasks.DayPoint) burnChart {
	c := burnChart{W: 720, H: 200, Max: 100}
	if len(days) < 2 {
		c.Empty = true
		return c
	}
	padL, padB, padT := 36, 24, 10
	w, h := c.W-padL-10, c.H-padB-padT
	x := func(i int) int { return padL + i*w/(len(days)-1) }
	y := func(p int) int { return padT + h - p*h/100 }
	pts := make([]string, len(days))
	step := max(1, len(days)/10)
	for i, d := range days {
		pts[i] = fmt.Sprintf("%d,%d", x(i), y(d.Percent))
		if i%step == 0 || i == len(days)-1 {
			c.Labels = append(c.Labels, burnLabel{X: x(i), Text: d.Day.Format("02.01")})
		}
	}
	c.Real = strings.Join(pts, " ")
	return c
}
