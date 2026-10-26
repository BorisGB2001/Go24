package web

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// Gantt geometry is computed here and drawn as inline SVG by templates/gantt.html.
// Colors come from CSS classes (theme variables), so the chart follows the theme.

type ganttBar struct {
	tasks.GanttTask
	X, Y, W   int
	Mid       int // vertical middle of the row
	Milestone bool
}

type ganttTick struct {
	X       int
	Label   string
	Weekend bool
}

type ganttMonth struct {
	X, W  int
	Month int // 1..12, label from the locale (month.N)
	Year  int
}

type ganttLink struct {
	D   string // SVG path
	Bad bool   // the successor starts before the predecessor ends
	tasks.Link
}

type ganttView struct {
	Scale             string // day | week | month
	DayW, RowH, HeadH int
	Width, Height     int
	Months            []ganttMonth
	Ticks             []ganttTick
	Bars              []ganttBar
	Links             []ganttLink
	TodayX            int
	ShowToday         bool
	Empty             bool
}

var ganttScales = map[string]int{"day": 26, "week": 9, "month": 3}

func buildGantt(g tasks.GanttData, scale string, today time.Time) ganttView {
	v := ganttView{RowH: 34, HeadH: 46}
	if len(g.Tasks) == 0 {
		v.Empty = true
		return v
	}
	sort.SliceStable(g.Tasks, func(i, j int) bool {
		if !g.Tasks[i].Start.Equal(g.Tasks[j].Start) {
			return g.Tasks[i].Start.Before(g.Tasks[j].Start)
		}
		return g.Tasks[i].Number < g.Tasks[j].Number
	})
	from, to := g.Tasks[0].Start, g.Tasks[0].End
	for _, t := range g.Tasks {
		if t.Start.Before(from) {
			from = t.Start
		}
		if t.End.After(to) {
			to = t.End
		}
	}
	y, m, d := today.Date()
	todayDay := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if todayDay.Before(from) {
		from = todayDay
	}
	if todayDay.After(to) {
		to = todayDay
	}
	from, to = from.AddDate(0, 0, -2), to.AddDate(0, 0, 3)
	days := int(to.Sub(from).Hours()/24) + 1
	if _, ok := ganttScales[scale]; !ok { // auto: keep the chart readable
		switch {
		case days <= 60:
			scale = "day"
		case days <= 400:
			scale = "week"
		default:
			scale = "month"
		}
	}
	v.Scale, v.DayW = scale, ganttScales[scale]
	v.Width = days * v.DayW
	v.Height = v.HeadH + len(g.Tasks)*v.RowH
	x := func(t time.Time) int { return int(t.Sub(from).Hours()/24) * v.DayW }

	// header: months, and days or week starts
	for cur := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); !cur.After(to); cur = cur.AddDate(0, 1, 0) {
		start, end := cur, cur.AddDate(0, 1, 0)
		if start.Before(from) {
			start = from
		}
		if end.After(to.AddDate(0, 0, 1)) {
			end = to.AddDate(0, 0, 1)
		}
		v.Months = append(v.Months, ganttMonth{X: x(start), W: x(end) - x(start), Month: int(cur.Month()), Year: cur.Year()})
	}
	for cur := from; !cur.After(to); cur = cur.AddDate(0, 0, 1) {
		wd := cur.Weekday()
		switch scale {
		case "day":
			v.Ticks = append(v.Ticks, ganttTick{X: x(cur), Label: fmt.Sprint(cur.Day()), Weekend: wd == time.Saturday || wd == time.Sunday})
		case "week":
			if wd == time.Monday {
				v.Ticks = append(v.Ticks, ganttTick{X: x(cur), Label: fmt.Sprint(cur.Day())})
			}
		}
	}

	pos := map[string]int{}
	for i, t := range g.Tasks {
		b := ganttBar{GanttTask: t, X: x(t.Start), Y: v.HeadH + i*v.RowH + 8, Mid: v.HeadH + i*v.RowH + v.RowH/2}
		b.W = x(t.End) - b.X + v.DayW
		b.Milestone = t.Start.Equal(t.End) && t.Planned
		pos[t.ID] = i
		v.Bars = append(v.Bars, b)
	}
	for _, l := range g.Links {
		fi, ok1 := pos[l.FromID]
		ti, ok2 := pos[l.ToID]
		if !ok1 || !ok2 {
			continue // one side is hidden (completed tasks not shown)
		}
		f, t := v.Bars[fi], v.Bars[ti]
		fx, tx := f.X+f.W, t.X
		elbow := fx + 6
		path := fmt.Sprintf("M%d %d H%d V%d H%d", fx, f.Mid, elbow, t.Mid, tx-2)
		if tx-2 < elbow { // successor starts left of the predecessor's end: route around
			path = fmt.Sprintf("M%d %d H%d V%d H%d V%d H%d", fx, f.Mid, elbow, t.Mid-v.RowH/2+2, tx-8, t.Mid, tx-2)
		}
		v.Links = append(v.Links, ganttLink{D: path, Bad: !t.Start.After(f.End), Link: l})
	}
	v.TodayX, v.ShowToday = x(todayDay)+v.DayW/2, true
	return v
}

func (s *Server) gantt(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	all := r.URL.Query().Get("all") == "1"
	g, err := s.Tasks.Gantt(r.Context(), sess.UserID, p.ID, all)
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	s.render(w, r, "gantt", page{Title: p.Name, Nav: projectNav(p), Data: map[string]any{
		"Project": p, "View": buildGantt(g, r.URL.Query().Get("scale"), time.Now()), "All": all,
	}})
}

// ---------- links on the task card ----------

func (s *Server) linkAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#links", func(id string) error {
		return s.Tasks.AddLink(r.Context(), sess.UserID, id, r.FormValue("pred"))
	})
}

func (s *Server) linkDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#links", func(id string) error {
		return s.Tasks.RemoveLink(r.Context(), sess.UserID, id, r.PathValue("id"))
	})
}
