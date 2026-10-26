package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
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
	DeadlineX int    // the deadline mark (end of its day), when the bar shows plan dates; 0 — none
	Depth     int    // the level in the tree of subtasks
	Kids      bool   // has subtasks on the chart
	Folded    bool   // its subtasks are hidden
	Fold      string // the «fold» parameter that toggles this bar's subtasks
}

func (b ganttBar) StartISO() string { return b.Start.Format("2006-01-02") }
func (b ganttBar) EndISO() string   { return b.End.Format("2006-01-02") }

// DotL, DotR: the handles of the bar's start and finish, dragged to draw a link.
func (b ganttBar) DotL() int { return b.X - 5 }
func (b ganttBar) DotR() int { return b.X + b.W + 5 }

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

// ganttTree orders the tasks Gantt does: each subtask right under its parent task, siblings by start;
// subtasks of the folded tasks (keys) are left out.
func ganttTree(list []tasks.GanttTask, folded map[string]bool) (out []ganttBar) {
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].Start.Equal(list[j].Start) {
			return list[i].Start.Before(list[j].Start)
		}
		return list[i].Number < list[j].Number
	})
	keys := map[string]bool{}
	for _, t := range list {
		keys[t.Key()] = true
	}
	kids := map[string][]tasks.GanttTask{}
	var roots []tasks.GanttTask
	for _, t := range list {
		if t.ParentKey != "" && keys[t.ParentKey] && t.ParentKey != t.Key() {
			kids[t.ParentKey] = append(kids[t.ParentKey], t)
		} else {
			roots = append(roots, t)
		}
	}
	seen := map[string]bool{}
	var walk func(ts []tasks.GanttTask, depth int)
	walk = func(ts []tasks.GanttTask, depth int) {
		for _, t := range ts {
			if seen[t.Key()] {
				continue
			}
			seen[t.Key()] = true
			b := ganttBar{GanttTask: t, Depth: depth, Kids: len(kids[t.Key()]) > 0, Folded: folded[t.Key()]}
			out = append(out, b)
			if !b.Folded {
				walk(kids[t.Key()], depth+1)
			}
		}
	}
	walk(roots, 0)
	return out
}

// foldParam is the «fold» parameter with the key toggled.
func foldParam(folded map[string]bool, key string) string {
	var keys []string
	for k, on := range folded {
		if on && k != key {
			keys = append(keys, k)
		}
	}
	if !folded[key] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func buildGantt(g tasks.GanttData, scale string, today time.Time, folded map[string]bool) ganttView {
	v := ganttView{RowH: 34, HeadH: 46}
	if len(g.Tasks) == 0 {
		v.Empty = true
		return v
	}
	rows := ganttTree(g.Tasks, folded)
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
	v.Height = v.HeadH + len(rows)*v.RowH
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
	for i, b := range rows {
		t := b.GanttTask
		b.X, b.Y, b.Mid = x(t.Start), v.HeadH+i*v.RowH+8, v.HeadH+i*v.RowH+v.RowH/2
		b.W = x(t.End) - b.X + v.DayW
		b.Milestone = t.Start.Equal(t.End) && t.Planned
		if t.Planned && t.Deadline != nil {
			yy, mm, dd := t.Deadline.Local().Date()
			if dl := time.Date(yy, mm, dd, 0, 0, 0, 0, time.UTC); !dl.Before(from) && !dl.After(to) {
				b.DeadlineX = x(dl) + v.DayW
			}
		}
		if b.Kids {
			b.Fold = foldParam(folded, t.Key())
		}
		pos[t.ID] = i
		v.Bars = append(v.Bars, b)
	}
	for _, l := range g.Links {
		fi, ok1 := pos[l.FromID]
		ti, ok2 := pos[l.ToID]
		if !ok1 || !ok2 {
			continue // one side is hidden (completed tasks not shown, folded subtasks)
		}
		f, t := v.Bars[fi], v.Bars[ti]
		path, bad := ganttLinkPath(l.Type, f, t, v.RowH)
		v.Links = append(v.Links, ganttLink{D: path, Bad: bad, Link: l})
	}
	v.TodayX, v.ShowToday = x(todayDay)+v.DayW/2, true
	return v
}

// ganttLinkPath draws a link of the type from the side of bar f to the side of bar t: out of f by a short elbow,
// into t from the outside of its side; when t's side is on the wrong hand, the line goes round along the row border.
// bad: the dates break the link (finish_start — t starts the day after f ends at the earliest).
func ganttLinkPath(typ string, f, t ganttBar, rowH int) (string, bool) {
	fromStart, toFinish := strings.HasPrefix(typ, "start_"), strings.HasSuffix(typ, "_finish")
	sx, out := f.X+f.W, 6
	if fromStart {
		sx, out = f.X, -6
	}
	tx, in := t.X-2, -6 // the arrow comes into the start from the left
	if toFinish {
		tx, in = t.X+t.W+2, 6 // into the finish from the right
	}
	elbow := sx + out
	path := fmt.Sprintf("M%d %d H%d V%d H%d", sx, f.Mid, elbow, t.Mid, tx)
	if (!toFinish && tx < elbow) || (toFinish && tx > elbow) {
		path = fmt.Sprintf("M%d %d H%d V%d H%d V%d H%d", sx, f.Mid, elbow, t.Mid-rowH/2+2, tx+in, t.Mid, tx)
	}
	var bad bool
	switch typ {
	case "start_start":
		bad = t.Start.Before(f.Start)
	case "finish_finish":
		bad = t.End.Before(f.End)
	case "start_finish":
		bad = t.End.Before(f.Start)
	default:
		bad = !t.Start.After(f.End)
	}
	return path, bad
}

func (s *Server) gantt(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	q := r.URL.Query()
	all := q.Get("all") == "1"
	g, err := s.Tasks.Gantt(r.Context(), sess.UserID, p, all)
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	folded := map[string]bool{}
	for _, k := range strings.Split(q.Get("fold"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			folded[k] = true
		}
	}
	v := buildGantt(g, q.Get("scale"), time.Now(), folded)
	s.render(w, r, "gantt", page{Title: p.Name, Nav: projectNav(p), Data: map[string]any{
		"Project": p, "View": v, "All": all, "Fold": q.Get("fold"), "LinkTypes": tasks.LinkTypes,
	}})
}

// ganttDates moves a bar of the Gantt chart: the task's plan dates (start_plan, end_plan).
func (s *Server) ganttDates(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#plan", func(id string) error {
		start, err := parseDate(r.FormValue("start_plan"))
		if err != nil {
			return err
		}
		end, err := parseDate(r.FormValue("end_plan"))
		if err != nil {
			return err
		}
		return s.Tasks.SetPlanDates(r.Context(), sess.UserID, id, start, end)
	})
}

// ---------- links on the task card ----------

func (s *Server) linkAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#links", func(id string) error {
		return s.Tasks.AddLink(r.Context(), sess.UserID, id, r.FormValue("pred"), r.FormValue("type"))
	})
}

func (s *Server) linkDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#links", func(id string) error {
		return s.Tasks.RemoveLink(r.Context(), sess.UserID, id, r.PathValue("id"))
	})
}
