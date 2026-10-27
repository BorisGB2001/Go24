package company

import (
	"context"
	"slices"
	"sync"
	"time"
)

// «Пульс компании»: how much the employees use go24 — who was active, in which tools, the index of
// involvement (the share of employees working in PulseInvolved tools or more), by tool, by day and by department.
// A day of a tool is marked once per employee (MarkTool); the administrator sees the whole company, a head or a
// deputy — their departments with sub-departments.

// PulseTools are the tools the pulse tells apart, in the order of the page; "mobile" is go24 opened on a phone,
// a channel rather than a tool, so it does not count to the involvement.
var PulseTools = []string{"tasks", "im", "feed", "calendar", "drive", "docs", "mail", "crm", "telephony", "openlines",
	"kb", "bp", "company", "collabs", "store", "sites", "marketing", "booking", "sign", "bi", "mobile"}

// PulseInvolved is how many tools make an employee involved counts the index.
const PulseInvolved = 4

// pulseSeen keeps the marks written today, so a page view costs no query once the day of the tool is marked.
type pulseSeen struct {
	mu   sync.Mutex
	day  string
	seen map[string]bool
}

// MarkTool notes that the employee used the tool on the day (local date); repeated marks of a day are free.
func (s *Store) MarkTool(ctx context.Context, userID, tool string, now time.Time) error {
	day := now.Format(time.DateOnly)
	key := userID + " " + tool
	s.seen.mu.Lock()
	if s.seen.day != day {
		s.seen.day, s.seen.seen = day, map[string]bool{}
	}
	done := s.seen.seen[key]
	s.seen.mu.Unlock()
	if done {
		return nil
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO user_activity (day, user_id, tool) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		day, userID, tool); err != nil {
		return err
	}
	s.seen.mu.Lock()
	if s.seen.day == day {
		s.seen.seen[key] = true
	}
	s.seen.mu.Unlock()
	return nil
}

// Pulse is the report of the page.
type Pulse struct {
	From, To time.Time
	Total    int  // employees in the scope
	Active   int  // used go24 within the period
	Today    int  // used it today (when the period ends today)
	HasToday bool // the period ends today
	Involved int  // used PulseInvolved tools or more within the period
	Days     []PulseDay
	Tools    []PulseTool
	Depts    []PulseDept
	Users    []PulseUser
}

// Pct is a share of the employees of the scope, in percent.
func (p *Pulse) Pct(n int) int { return pct(n, p.Total) }

// Index is the index of involvement, in percent.
func (p *Pulse) Index() int { return pct(p.Involved, p.Total) }

// MaxDay is the busiest day, for the scale of the chart.
func (p *Pulse) MaxDay() int {
	m := 0
	for _, d := range p.Days {
		m = max(m, d.Users)
	}
	return m
}

func pct(n, of int) int {
	if of == 0 {
		return 0
	}
	return (n*100 + of/2) / of
}

// PulseDay: how many employees were active on the day.
type PulseDay struct {
	Day   time.Time
	Users int
}

// PulseTool: how many employees used the tool within the period, and on each day of it.
type PulseTool struct {
	Tool  string
	Users int
	Pct   int
	Daily []int
}

// PulseDept is a department with its sub-departments.
type PulseDept struct {
	ID       string
	Name     string
	Depth    int
	Total    int
	Active   int
	Involved int
}

func (d PulseDept) Pct() int   { return pct(d.Active, d.Total) }
func (d PulseDept) Index() int { return pct(d.Involved, d.Total) }

// PulseUser is an employee of the scope with the tools they used and on how many days.
type PulseUser struct {
	Person
	Departments []string
	Tools       []string // in the order of PulseTools
	Days        int
	Involved    bool
}

// PulseScope: the departments whose pulse the person sees — nil with all=true for an administrator (the whole
// company), the departments they head or are a deputy of otherwise (empty — none, the page is closed).
func (s *Store) PulseScope(ctx context.Context, userID string, admin bool) (ids []string, all bool, err error) {
	if admin {
		return nil, true, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id::text FROM departments WHERE head_id = $1
		UNION SELECT department_id::text FROM department_deputies WHERE user_id = $1`, userID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	return ids, false, rows.Err()
}

// Pulse builds the report for the days from–to (local dates, inclusive) and the departments of the scope with their
// sub-departments (all — the whole company); dept narrows it to one department of the scope; today is the local date.
func (s *Store) Pulse(ctx context.Context, from, to, today time.Time, scope []string, all bool, dept string) (*Pulse, error) {
	_, flat, err := s.Tree(ctx)
	if err != nil {
		return nil, err
	}
	children := map[string][]*Department{}
	for _, d := range flat {
		children[d.ParentID] = append(children[d.ParentID], d)
	}
	// the departments shown: the scope with sub-departments, in the order of the tree
	shown := map[string]bool{}
	var mark func(id string)
	mark = func(id string) {
		if shown[id] {
			return
		}
		shown[id] = true
		for _, c := range children[id] {
			mark(c.ID)
		}
	}
	for _, d := range flat {
		if all || slices.Contains(scope, d.ID) {
			mark(d.ID)
		}
	}
	if dept != "" {
		if !shown[dept] {
			return nil, ErrForbidden
		}
		clear(shown)
		mark(dept)
	}
	// the people of each department with its sub-departments
	people := map[string]map[string]bool{}
	var collect func(d *Department) map[string]bool
	collect = func(d *Department) map[string]bool {
		set := map[string]bool{}
		for _, p := range d.Employees {
			set[p.ID] = true
		}
		for _, c := range children[d.ID] {
			for id := range collect(c) {
				set[id] = true
			}
		}
		people[d.ID] = set
		return set
	}
	for _, d := range flat {
		if d.ParentID == "" {
			collect(d)
		}
	}
	list, err := s.Employees(ctx, Filter{})
	if err != nil {
		return nil, err
	}
	inScope := func(id string) bool {
		if all && dept == "" {
			return true
		}
		for d := range shown {
			if people[d][id] {
				return true
			}
		}
		return false
	}
	p := &Pulse{From: from, To: to, HasToday: to.Equal(today)}
	var ids []string
	for _, e := range list {
		if !inScope(e.ID) {
			continue
		}
		ids = append(ids, e.ID)
		u := PulseUser{Person: Person{ID: e.ID, Name: e.FullName(), Position: e.Position, PhotoKey: e.PhotoKey}}
		for _, d := range e.Departments {
			u.Departments = append(u.Departments, d.Name)
		}
		p.Users = append(p.Users, u)
	}
	p.Total = len(p.Users)
	member := map[string]bool{}
	for _, id := range ids {
		member[id] = true
	}
	var days []time.Time
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	dayIdx := map[string]int{}
	for i, d := range days {
		dayIdx[d.Format(time.DateOnly)] = i
	}
	rows, err := s.db.Query(ctx, `SELECT day, user_id::text, tool FROM user_activity
		WHERE day BETWEEN $1 AND $2 AND user_id = ANY($3)`, from.Format(time.DateOnly), to.Format(time.DateOnly), ids)
	if err != nil {
		return nil, err
	}
	type dayUser struct {
		day  int
		user string
	}
	toolUsers := map[string]map[string]bool{}
	toolDay := map[string][]map[string]bool{}
	activeDay := map[dayUser]bool{}
	userTools := map[string]map[string]bool{}
	for rows.Next() {
		var day time.Time
		var user, tool string
		if err := rows.Scan(&day, &user, &tool); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := dayIdx[day.Format(time.DateOnly)] // a date of pgx is midnight UTC: its digits are the day
		if !ok {
			continue
		}
		if toolUsers[tool] == nil {
			toolUsers[tool] = map[string]bool{}
			toolDay[tool] = make([]map[string]bool, len(days))
		}
		toolUsers[tool][user] = true
		if toolDay[tool][i] == nil {
			toolDay[tool][i] = map[string]bool{}
		}
		toolDay[tool][i][user] = true
		activeDay[dayUser{i, user}] = true
		if userTools[user] == nil {
			userTools[user] = map[string]bool{}
		}
		userTools[user][tool] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	p.Days = make([]PulseDay, len(days))
	for i, d := range days {
		p.Days[i].Day = d
	}
	userDays := map[string]int{}
	for k := range activeDay {
		p.Days[k.day].Users++
		userDays[k.user]++
	}
	if p.HasToday && len(days) > 0 {
		p.Today = p.Days[len(days)-1].Users
	}
	for _, t := range PulseTools {
		pt := PulseTool{Tool: t, Users: len(toolUsers[t]), Daily: make([]int, len(days))}
		pt.Pct = p.Pct(pt.Users)
		for i, set := range toolDay[t] {
			pt.Daily[i] = len(set)
		}
		p.Tools = append(p.Tools, pt)
	}
	involved := map[string]bool{}
	for i := range p.Users {
		u := &p.Users[i]
		u.Days = userDays[u.ID]
		n := 0
		for _, t := range PulseTools {
			if userTools[u.ID][t] {
				u.Tools = append(u.Tools, t)
				if t != "mobile" {
					n++
				}
			}
		}
		u.Involved = n >= PulseInvolved
		if u.Days > 0 {
			p.Active++
		}
		if u.Involved {
			p.Involved++
			involved[u.ID] = true
		}
	}
	slices.SortStableFunc(p.Users, func(a, b PulseUser) int { return len(b.Tools) - len(a.Tools) })
	for _, d := range flat {
		if !shown[d.ID] {
			continue
		}
		pd := PulseDept{ID: d.ID, Name: d.Name, Depth: d.Depth}
		for id := range people[d.ID] {
			if !member[id] {
				continue
			}
			pd.Total++
			if userDays[id] > 0 {
				pd.Active++
			}
			if involved[id] {
				pd.Involved++
			}
		}
		p.Depts = append(p.Depts, pd)
	}
	return p, nil
}
