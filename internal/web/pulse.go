package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// pulseTools: the first segment of a page's path → the tool of «Пульс компании».
var pulseTools = map[string]string{
	"my": "tasks", "tasks": "tasks", "t": "tasks", "p": "tasks", "projects": "tasks", "flows": "tasks", "templates": "tasks",
	"plan": "tasks", "deadlines": "tasks",
	"im": "im", "feed": "feed", "calendar": "calendar",
	"files": "drive", "disk": "drive", "boards": "drive", "onlyoffice": "drive",
	"docs": "docs", "mail": "mail", "crm": "crm",
	"telephony": "telephony", "phone": "telephony", "call": "telephony",
	"openlines": "openlines", "kb": "kb", "kbp": "kb", "bp": "bp",
	"company": "company", "worktime": "company", "u": "company", "people": "company",
	"collabs": "collabs", "c": "collabs",
	"store": "store", "sites": "sites", "marketing": "marketing", "booking": "booking", "sign": "sign", "bi": "bi",
}

// pulseTool is the tool of the request's page ("" — none: settings, notifications, files of the pages…).
func pulseTool(path string) string {
	first, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return pulseTools[first]
}

// isPhone: the browser is a phone's (go24 on the go, «Мобильное приложение» of the pulse).
func isPhone(r *http.Request) bool {
	ua := r.UserAgent()
	return strings.Contains(ua, "Mobile") || strings.Contains(ua, "Android") || strings.Contains(ua, "iPhone")
}

// markPulse notes the employee's day in the tool of the page they opened; guests are not employees.
func (s *Server) markPulse(r *http.Request, sess auth.Session, guest bool) {
	if s.Company == nil || guest || r.Method != http.MethodGet {
		return
	}
	now := time.Now()
	tool := pulseTool(r.URL.Path)
	if tool != "" {
		if err := s.Company.MarkTool(r.Context(), sess.UserID, tool, now); err != nil {
			s.Log.Warn("pulse mark", "err", err)
		}
	}
	if isPhone(r) {
		if err := s.Company.MarkTool(r.Context(), sess.UserID, "mobile", now); err != nil {
			s.Log.Warn("pulse mark", "err", err)
		}
	}
}

// pulsePeriods are the quick periods of the page, in days back from today.
var pulsePeriods = map[string]int{"today": 1, "week": 7, "month": 30, "quarter": 90}

// pulseQuery reads the period (?period=today|week|month|quarter, or ?from=&to=, at most a year) and the department.
func pulseQuery(r *http.Request) (from, to, today time.Time, period string, err error) {
	now := time.Now()
	today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	q := r.URL.Query()
	period = q.Get("period")
	if q.Get("from") != "" || q.Get("to") != "" {
		period = ""
		if from, err = time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local); err != nil {
			return from, to, today, period, i18n.E("err.bad_form")
		}
		if to, err = time.ParseInLocation(time.DateOnly, q.Get("to"), time.Local); err != nil || to.Before(from) ||
			to.After(from.AddDate(1, 0, 0)) {
			return from, to, today, period, i18n.E("err.bad_form")
		}
		return from, to, today, period, nil
	}
	if pulsePeriods[period] == 0 {
		period = "week"
	}
	return today.AddDate(0, 0, 1-pulsePeriods[period]), today, today, period, nil
}

// pulse builds the report for the viewer: the administrator sees the company, a head — their departments.
func (s *Server) pulse(r *http.Request, userID string, admin bool, from, to, today time.Time, dept string) (*company.Pulse, error) {
	scope, all, err := s.Company.PulseScope(r.Context(), userID, admin)
	if err != nil {
		return nil, err
	}
	if !all && len(scope) == 0 {
		return nil, company.ErrForbidden
	}
	if _, _, err := s.tree(r); err != nil {
		return nil, err
	}
	return s.Company.Pulse(r.Context(), from, to, today, scope, all, dept)
}

// pulsePage: GET /company/pulse — «Пульс компании» (?period, ?from&to, ?dept).
func (s *Server) pulsePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	from, to, today, period, err := pulseQuery(r)
	if err != nil {
		s.taskErr(w, r, err, "/company/pulse")
		return
	}
	dept := r.URL.Query().Get("dept")
	p, err := s.pulse(r, sess.UserID, sess.Admin, from, to, today, dept)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	data := map[string]any{"P": p, "Period": period, "Dept": dept, "Involved": company.PulseInvolved,
		"Periods": []string{"today", "week", "month", "quarter"}}
	s.render(w, r, "pulse", page{TitleKey: "pulse.title", Nav: "pulse", Data: data})
}

// feedPulse is the card of the feed's right column for those who see the pulse: today's activity and involvement.
func (s *Server) feedPulse(r *http.Request, sess auth.Session) *company.Pulse {
	if s.Company == nil {
		return nil
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	p, err := s.pulse(r, sess.UserID, sess.Admin, today, today, today, "")
	if err != nil {
		if !errors.Is(err, company.ErrForbidden) {
			s.Log.Warn("feed pulse", "err", err)
		}
		return nil
	}
	return p
}

type apiPulseTool struct {
	Tool    string `json:"tool"`
	Users   int    `json:"users"`
	Percent int    `json:"percent"`
	Daily   []int  `json:"daily"`
}

type apiPulseDept struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Depth    int    `json:"depth"`
	Total    int    `json:"total"`
	Active   int    `json:"active"`
	Involved int    `json:"involved"`
	Index    int    `json:"index"`
}

type apiPulseUser struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Position    string   `json:"position"`
	Departments []string `json:"departments"`
	Tools       []string `json:"tools"`
	Days        int      `json:"days"`
	Involved    bool     `json:"involved"`
}

type apiPulseDay struct {
	Day   string `json:"day"`
	Users int    `json:"users"`
}

// GET /api/v1/pulse — «Пульс компании» for the key owner (an administrator — the company, a head — their departments):
// ?period=today|week|month|quarter or ?from=&to= (dates), ?dept=.
func (s *Server) apiPulse(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to, today, _, err := pulseQuery(r)
	if err != nil {
		return err
	}
	pl, err := s.pulse(r, p.UserID, s.apiAdmin(r.Context(), p.UserID), from, to, today, r.URL.Query().Get("dept"))
	if err != nil {
		return err
	}
	out := map[string]any{"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly), "total": pl.Total,
		"active": pl.Active, "involved": pl.Involved, "index": pl.Index(), "involved_tools": company.PulseInvolved}
	if pl.HasToday {
		out["today"] = pl.Today
	}
	days := make([]apiPulseDay, 0, len(pl.Days))
	for _, d := range pl.Days {
		days = append(days, apiPulseDay{d.Day.Format(time.DateOnly), d.Users})
	}
	tools := make([]apiPulseTool, 0, len(pl.Tools))
	for _, t := range pl.Tools {
		tools = append(tools, apiPulseTool{t.Tool, t.Users, t.Pct, t.Daily})
	}
	depts := make([]apiPulseDept, 0, len(pl.Depts))
	for _, d := range pl.Depts {
		depts = append(depts, apiPulseDept{d.ID, d.Name, d.Depth, d.Total, d.Active, d.Involved, d.Index()})
	}
	users := make([]apiPulseUser, 0, len(pl.Users))
	for _, u := range pl.Users {
		users = append(users, apiPulseUser{u.ID, u.Name, u.Position, append([]string{}, u.Departments...), append([]string{}, u.Tools...), u.Days, u.Involved})
	}
	out["days"], out["tools"], out["departments"], out["users"] = days, tools, depts, users
	writeJSON(w, http.StatusOK, out)
	return nil
}
