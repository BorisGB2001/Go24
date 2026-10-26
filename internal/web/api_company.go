package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/company"
	"go24/internal/i18n"
)

// ---------- REST of «Компания» (user.*, department.*, timeman.*) ----------
//
// user.get, user.search             — /api/v1/users, /api/v1/users/{id}
// department.get/add/update/delete  — /api/v1/departments (changed by administrators)
// «Команды» of the structure          — /api/v1/teams (administrators; a team's head changes its people)
// timeman.status/open/pause/close   — /api/v1/worktime/status, /api/v1/worktime/{action} (the key owner's own day)
// timeman.timecontrol.reports.get   — /api/v1/worktime/days (another person's days — for their managers)
// timeman.schedule.get              — /api/v1/worktime/schedules, /api/v1/users/{id}/schedule
// «График отсутствий»               — /api/v1/absences (with the rights of the page)
// user.add (an invitation by e-mail) — /api/v1/users/invite, /api/v1/users/invites (who may invite)
// «Доска почёта»                    — /api/v1/honors (kept by administrators and HR officers with the right)

func (s *Server) apiCompanyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/users", s.api("company:read", s.apiUsers))
	mux.HandleFunc("GET /api/v1/users/{id}", s.api("company:read", s.apiUser))
	mux.HandleFunc("POST /api/v1/users/invite", s.api("company:write", s.apiInvite))
	mux.HandleFunc("GET /api/v1/users/invites", s.api("company:read", s.apiInvites))
	mux.HandleFunc("POST /api/v1/users/invites/{id}/resend", s.api("company:write", s.apiInviteResend))
	mux.HandleFunc("DELETE /api/v1/users/invites/{id}", s.api("company:write", s.apiInviteCancel))
	mux.HandleFunc("GET /api/v1/users/{id}/schedule", s.api("company:read", s.apiUserSchedule))
	mux.HandleFunc("GET /api/v1/departments", s.api("company:read", s.apiDepartments))
	mux.HandleFunc("POST /api/v1/departments", s.api("company:write", s.apiDepartmentAdd))
	mux.HandleFunc("PATCH /api/v1/departments/{id}", s.api("company:write", s.apiDepartmentUpdate))
	mux.HandleFunc("DELETE /api/v1/departments/{id}", s.api("company:write", s.apiDepartmentDelete))
	mux.HandleFunc("GET /api/v1/teams", s.api("company:read", s.apiTeams))
	mux.HandleFunc("GET /api/v1/teams/{id}", s.api("company:read", s.apiTeam))
	mux.HandleFunc("POST /api/v1/teams", s.api("company:write", s.apiTeamAdd))
	mux.HandleFunc("PATCH /api/v1/teams/{id}", s.api("company:write", s.apiTeamUpdate))
	mux.HandleFunc("DELETE /api/v1/teams/{id}", s.api("company:write", s.apiTeamDelete))
	mux.HandleFunc("GET /api/v1/worktime/status", s.api("company:read", s.apiWorkStatus))
	mux.HandleFunc("POST /api/v1/worktime/{action}", s.api("company:write", s.apiWorkAction))
	mux.HandleFunc("GET /api/v1/worktime/days", s.api("company:read", s.apiWorkdays))
	mux.HandleFunc("GET /api/v1/worktime/schedules", s.api("company:read", s.apiWorkSchedules))
	mux.HandleFunc("GET /api/v1/worktime/schedules/{id}", s.api("company:read", s.apiWorkSchedule))
	mux.HandleFunc("GET /api/v1/absences", s.api("company:read", s.apiAbsences))
	mux.HandleFunc("POST /api/v1/absences", s.api("company:write", s.apiAbsenceAdd))
	mux.HandleFunc("DELETE /api/v1/absences/{id}", s.api("company:write", s.apiAbsenceDelete))
	mux.HandleFunc("GET /api/v1/honors", s.api("company:read", s.apiHonors))
	mux.HandleFunc("POST /api/v1/honors", s.api("company:write", s.apiHonorAdd))
	mux.HandleFunc("DELETE /api/v1/honors/{id}", s.api("company:write", s.apiHonorDelete))
}

type apiDeptRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiUserOut struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	LastName    string       `json:"last_name,omitempty"`
	FirstName   string       `json:"first_name,omitempty"`
	SecondName  string       `json:"second_name,omitempty"`
	Email       string       `json:"email,omitempty"`
	Position    string       `json:"position,omitempty"`
	WorkPhone   string       `json:"work_phone,omitempty"`
	MobilePhone string       `json:"mobile_phone,omitempty"`
	InnerPhone  string       `json:"inner_phone,omitempty"`
	Birthday    string       `json:"birthday,omitempty"` // 1990-05-12, or 05-12 when the year is hidden
	Gender      string       `json:"gender,omitempty"`
	City        string       `json:"city,omitempty"`
	TimeZone    string       `json:"time_zone,omitempty"`
	Employment  string       `json:"employment_date,omitempty"`
	Telegram    string       `json:"telegram,omitempty"`
	WhatsApp    string       `json:"whatsapp,omitempty"`
	Website     string       `json:"website,omitempty"`
	Username    string       `json:"username,omitempty"`
	About       string       `json:"about,omitempty"`
	Active      bool         `json:"active"`
	Guest       bool         `json:"guest,omitempty"`
	LastLogin   *time.Time   `json:"last_login,omitempty"`
	PhotoURL    string       `json:"photo_url,omitempty"`
	Departments []apiDeptRef `json:"departments"`
	Teams       []apiDeptRef `json:"teams"`
}

func apiUserOf(p company.Profile, viewer string) apiUserOut {
	u := apiUserOut{ID: p.ID, Name: p.Name, LastName: p.LastName, FirstName: p.FirstName, SecondName: p.SecondName, Email: p.Email,
		Position: p.Position, WorkPhone: p.WorkPhone, MobilePhone: p.MobilePhone, InnerPhone: p.InnerPhone, Gender: p.Gender,
		City: p.City, TimeZone: p.TimeZone, Telegram: p.Telegram, WhatsApp: p.WhatsApp, Website: p.Website, Username: p.Username,
		About: p.About, Active: p.Active, Guest: p.Guest, Departments: []apiDeptRef{}, Teams: []apiDeptRef{}}
	if p.Birthday != nil {
		if p.BirthdayYear || p.ID == viewer {
			u.Birthday = p.Birthday.Format(time.DateOnly)
		} else {
			u.Birthday = p.Birthday.Format("01-02")
		}
	}
	if p.Employment != nil {
		u.Employment = p.Employment.Format(time.DateOnly)
	}
	if !p.LastLogin.IsZero() {
		u.LastLogin = &p.LastLogin
	}
	if p.PhotoKey != "" {
		u.PhotoURL = company.PhotoURL(p.ID, p.PhotoKey)
	}
	for _, d := range p.Departments {
		u.Departments = append(u.Departments, apiDeptRef{d.ID, d.Name})
	}
	for _, t := range p.Teams {
		u.Teams = append(u.Teams, apiDeptRef{t.ID, t.Name})
	}
	return u
}

// GET /api/v1/users?q=&department_id=&team_id=&fired=1&preset=here|absent|new&guests=1 — the employees (user.search);
// guests of collabs — for administrators.
func (s *Server) apiUsers(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	f := company.Filter{Query: q.Get("q"), DepartmentID: q.Get("department_id"), TeamID: q.Get("team_id"), Fired: q.Get("fired") == "1", Preset: q.Get("preset"),
		Guests: q.Get("guests") == "1"}
	if f.Preset != "" && !slices.Contains(company.EmployeePresets, f.Preset) {
		return i18n.E("err.bad_form")
	}
	if f.Guests && !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	list, err := s.Company.Employees(r.Context(), f)
	if err != nil {
		return err
	}
	out := make([]apiUserOut, 0, len(list))
	for _, x := range list {
		out = append(out, apiUserOf(x, p.UserID))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/users/{id} — the employee's card (user.get); "me" — the key's owner (user.current).
func (s *Server) apiUser(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := r.PathValue("id")
	if id == "me" {
		id = p.UserID
	}
	prof, err := s.Company.Profile(r.Context(), id)
	if err != nil {
		return i18n.E("err.not_found")
	}
	if prof.Guest && id != p.UserID && !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.not_found")
	}
	writeJSON(w, http.StatusOK, apiUserOf(prof, p.UserID))
	return nil
}

type apiPersonOut struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position string `json:"position,omitempty"`
}

type apiDeptOut struct {
	ID        string         `json:"id"`
	ParentID  string         `json:"parent_id,omitempty"`
	Name      string         `json:"name"`
	HeadID    string         `json:"head_id,omitempty"`
	HeadName  string         `json:"head_name,omitempty"`
	Deputies  []apiPersonOut `json:"deputies"`
	Employees []apiPersonOut `json:"employees"`
	Depth     int            `json:"depth"`
}

func apiPeople(list []company.Person) []apiPersonOut {
	out := make([]apiPersonOut, 0, len(list))
	for _, x := range list {
		out = append(out, apiPersonOut{x.ID, x.Name, x.Position})
	}
	return out
}

// apiTree is the structure; the company node is made on first use, as the page does.
func (s *Server) apiTree(r *http.Request) (*company.Department, []*company.Department, error) {
	if err := s.Company.EnsureRoot(r.Context(), s.apiTr(r)("company.root_default")); err != nil {
		return nil, nil, err
	}
	return s.Company.Tree(r.Context())
}

// GET /api/v1/departments — the company structure as a flat list in the order of the tree (department.get); the
// root, without parent_id, is the company itself.
func (s *Server) apiDepartments(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	_, flat, err := s.apiTree(r)
	if err != nil {
		return err
	}
	out := make([]apiDeptOut, 0, len(flat))
	for _, d := range flat {
		out = append(out, apiDeptOut{d.ID, d.ParentID, d.Name, d.HeadID, d.HeadName, apiPeople(d.Deputies), apiPeople(d.Employees), d.Depth})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type apiDeptIn struct {
	ParentID *string   `json:"parent_id"`
	Name     *string   `json:"name"`
	HeadID   *string   `json:"head_id"`
	Deputies *[]string `json:"deputies"`
}

// POST /api/v1/departments {"parent_id","name","head_id"} — a new department (department.add).
func (s *Server) apiDepartmentAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	var in apiDeptIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	str := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	parent := str(in.ParentID)
	if parent == "" { // under the company by default
		root, _, err := s.apiTree(r)
		if err != nil {
			return err
		}
		if root != nil {
			parent = root.ID
		}
	}
	id, err := s.Company.CreateDepartment(r.Context(), parent, str(in.Name), str(in.HeadID))
	if err != nil {
		return err
	}
	s.Log.Info("api department added", "user_id", p.UserID, "id", id)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
	return nil
}

// PATCH /api/v1/departments/{id} — changes the given fields (department.update): a new parent moves the department.
func (s *Server) apiDepartmentUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	var in apiDeptIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	_, flat, err := s.apiTree(r)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(flat, func(d *company.Department) bool { return d.ID == r.PathValue("id") })
	if i < 0 {
		return i18n.E("err.not_found")
	}
	d := flat[i]
	parent, name, head := d.ParentID, d.Name, d.HeadID
	deputies := make([]string, 0, len(d.Deputies))
	for _, x := range d.Deputies {
		deputies = append(deputies, x.ID)
	}
	if in.ParentID != nil {
		parent = *in.ParentID
	}
	if in.Name != nil {
		name = *in.Name
	}
	if in.HeadID != nil {
		head = *in.HeadID
	}
	if in.Deputies != nil {
		deputies = *in.Deputies
	}
	if err := s.Company.UpdateDepartment(r.Context(), d.ID, parent, name, head, deputies...); err != nil {
		return err
	}
	s.Log.Info("api department updated", "user_id", p.UserID, "id", d.ID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// DELETE /api/v1/departments/{id} — department.delete.
func (s *Server) apiDepartmentDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	if err := s.Company.DeleteDepartment(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	s.Log.Info("api department deleted", "user_id", p.UserID, "id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiWorkdayOut struct {
	UserID     string     `json:"user_id"`
	Day        string     `json:"day"`
	State      string     `json:"state"` // running | paused | done
	Started    time.Time  `json:"started"`
	Ended      *time.Time `json:"ended,omitempty"`
	PausedAt   *time.Time `json:"paused_at,omitempty"`
	PauseSecs  int        `json:"pause_secs"`
	WorkedSecs int        `json:"worked_secs"`
	Report     string     `json:"report,omitempty"`
	Violations []string   `json:"violations,omitempty"` // late | open | early | short — by the person's schedule
	Start      apiPlace   `json:"start"`
	End        *apiPlace  `json:"end,omitempty"`
}

// apiPlace is where the day was started or finished: "office" — the office network, "" outside the offices, absent
// when no office networks are set.
type apiPlace struct {
	IP     string  `json:"ip,omitempty"`
	Office *string `json:"office,omitempty"`
}

func apiWorkdayOf(d company.Workday, n company.Norm, now time.Time) apiWorkdayOut {
	out := apiWorkdayOut{UserID: d.UserID, Day: d.Day.Format(time.DateOnly), State: d.State(), Started: d.Started, Ended: d.Ended,
		PausedAt: d.PausedAt, PauseSecs: d.PauseSecs, WorkedSecs: d.Worked(now), Report: d.Report, Violations: n.Violations(d, now),
		Start: apiPlace(d.Start)}
	if d.Ended != nil {
		out.End = &apiPlace{d.End.IP, d.End.Office}
	}
	return out
}

type apiDayPlanOut struct {
	Work  bool   `json:"work"`
	Free  bool   `json:"free,omitempty"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Break int    `json:"break,omitempty"`
	Shift string `json:"shift,omitempty"`
}

// apiNorm is the person's working-time rules for [from, to).
func (s *Server) apiNorm(r *http.Request, userID string, from, to time.Time) (company.Norm, error) {
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		return company.Norm{}, err
	}
	return s.Company.NormOf(r.Context(), userID, sched, from, to)
}

// GET /api/v1/worktime/status — the key owner's working day of today (timeman.status): "state" is "none" before it
// starts; "plan" — today by their schedule.
func (s *Server) apiWorkStatus(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	n, err := s.apiNorm(r, p.UserID, day, day.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	pl := n.Plan(day)
	out := map[string]any{"state": "none", "day": day.Format(time.DateOnly),
		"plan": apiDayPlanOut{pl.Work, pl.Free, pl.Start, pl.End, pl.Break, pl.Shift}}
	d, err := s.Company.Today(r.Context(), p.UserID, now)
	if err != nil {
		return err
	}
	if d != nil {
		wd := apiWorkdayOf(*d, n, now)
		out["state"], out["workday"] = wd.State, wd
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/worktime/{start|pause|resume|finish|reopen} {"report"} — the key owner's working day (timeman.open,
// pause, close; reopen — «Продолжить рабочий день»).
func (s *Server) apiWorkAction(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Report string `json:"report"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	action := r.PathValue("action")
	at, err := s.workPlace(r, action)
	if err != nil {
		return err
	}
	if err := s.Company.WorkAction(r.Context(), p.UserID, action, in.Report, time.Now(), at); err != nil {
		return err
	}
	s.Log.Info("api workday", "user_id", p.UserID, "action", action)
	return s.apiWorkStatus(w, r, p)
}

// apiDates are ?from=&to= (YYYY-MM-DD, both included; by default — the current month).
func apiDates(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 1, -1)
	for _, x := range []struct {
		key string
		t   *time.Time
	}{{"from", &from}, {"to", &to}} {
		if v := r.URL.Query().Get(x.key); v != "" {
			t, err := time.ParseInLocation(time.DateOnly, v, time.Local)
			if err != nil {
				return from, to, i18n.E("err.bad_form")
			}
			*x.t = t
		}
	}
	if to.Before(from) || to.Sub(from) > 366*24*time.Hour {
		return from, to, i18n.E("err.bad_form")
	}
	return from, to, nil
}

// GET /api/v1/worktime/days?user_id=&from=&to= — the working days (timeman.timecontrol.reports.get): own, or of a
// person below the key owner (and anyone's — for administrators).
func (s *Server) apiWorkdays(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to, err := apiDates(r)
	if err != nil {
		return err
	}
	uid := r.URL.Query().Get("user_id")
	if uid == "" || uid == "me" {
		uid = p.UserID
	}
	if uid != p.UserID {
		ok, err := s.Company.Manages(r.Context(), p.UserID, uid, s.apiAdmin(r.Context(), p.UserID))
		if err != nil {
			return err
		}
		if !ok {
			return i18n.E("err.forbidden")
		}
	}
	end := to.AddDate(0, 0, 1)
	days, err := s.Company.Workdays(r.Context(), uid, from, end)
	if err != nil {
		return err
	}
	n, err := s.apiNorm(r, uid, from, end)
	if err != nil {
		return err
	}
	now := time.Now()
	out := make([]apiWorkdayOut, 0, len(days))
	for _, d := range days {
		out = append(out, apiWorkdayOf(d, n, now))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type apiShiftOut struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Start string `json:"start"`
	End   string `json:"end"`
	Break int    `json:"break"`
}

type apiScheduleOut struct {
	ID          int64                  `json:"id"`
	Name        string                 `json:"name"`
	Kind        string                 `json:"kind"` // fixed | shift | flexible
	Times       []company.ScheduleTime `json:"times,omitempty"`
	Shifts      []apiShiftOut          `json:"shifts,omitempty"`
	LateAfter   string                 `json:"late_after,omitempty"`
	LeaveBefore string                 `json:"leave_before,omitempty"`
	MinDuration string                 `json:"min_duration,omitempty"`
	Tolerance   int                    `json:"tolerance,omitempty"`
	Members     []string               `json:"members"`  // people's ids and departments as "dept:ID"
	Excluded    []string               `json:"excluded"` // people left out of the departments
	People      []apiPersonOut         `json:"people,omitempty"`
}

func apiScheduleOf(ws *company.WorkSchedule) apiScheduleOut {
	out := apiScheduleOut{ID: ws.ID, Name: ws.Name, Kind: ws.Kind, Times: ws.Times, LateAfter: ws.LateAfter, LeaveBefore: ws.LeaveBefore,
		MinDuration: ws.MinDuration, Tolerance: ws.Tolerance, Members: ws.Members, Excluded: ws.Excluded}
	for _, sh := range ws.Shifts {
		out.Shifts = append(out.Shifts, apiShiftOut{sh.ID, sh.Name, sh.Start, sh.End, sh.Break})
	}
	if out.Members == nil {
		out.Members = []string{}
	}
	if out.Excluded == nil {
		out.Excluded = []string{}
	}
	return out
}

// GET /api/v1/worktime/schedules — the work schedules.
func (s *Server) apiWorkSchedules(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Company.WorkSchedules(r.Context())
	if err != nil {
		return err
	}
	type item struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Depts  int    `json:"departments"`
		People int    `json:"people"`
	}
	out := make([]item, 0, len(list))
	for _, x := range list {
		out = append(out, item{x.ID, x.Name, x.Kind, x.Depts, x.People})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/worktime/schedules/{id} — the schedule with the people it applies to (timeman.schedule.get).
func (s *Server) apiWorkSchedule(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return i18n.E("err.not_found")
	}
	ws, err := s.Company.WorkSchedule(r.Context(), id)
	if err != nil {
		return err
	}
	people, err := s.Company.SchedulePeople(r.Context(), id)
	if err != nil {
		return err
	}
	out := apiScheduleOf(ws)
	out.People = apiPeople(people)
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/users/{id}/schedule?from=&to= — the person's schedule (null: the company's working time) and their
// days by it (timeman.schedule.get for a user).
func (s *Server) apiUserSchedule(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to, err := apiDates(r)
	if err != nil {
		return err
	}
	uid := r.PathValue("id")
	if uid == "me" {
		uid = p.UserID
	}
	if _, err := s.Company.Profile(r.Context(), uid); err != nil {
		return i18n.E("err.not_found")
	}
	n, err := s.apiNorm(r, uid, from, to.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	out := map[string]any{"schedule": nil}
	if n.WS != nil {
		out["schedule"] = apiScheduleOf(n.WS)
	}
	days := map[string]apiDayPlanOut{}
	for t := from; !t.After(to); t = t.AddDate(0, 0, 1) {
		pl := n.Plan(t)
		days[t.Format(time.DateOnly)] = apiDayPlanOut{pl.Work, pl.Free, pl.Start, pl.End, pl.Break, pl.Shift}
	}
	out["days"] = days
	writeJSON(w, http.StatusOK, out)
	return nil
}

type apiAbsenceOut struct {
	ID         string   `json:"id"`
	UserID     string   `json:"user_id"`
	UserName   string   `json:"user_name"`
	Kind       string   `json:"kind"` // vacation | sick | trip | dayoff | other
	From       string   `json:"from"`
	To         string   `json:"to"`
	Comment    string   `json:"comment,omitempty"`
	ActingID   string   `json:"acting_id,omitempty"`
	ActingName string   `json:"acting_name,omitempty"`
	Rights     []string `json:"acting_rights,omitempty"`
}

// GET /api/v1/absences?from=&to=&user_id= — «График отсутствий» for the dates (the current month by default).
func (s *Server) apiAbsences(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to, err := apiDates(r)
	if err != nil {
		return err
	}
	list, err := s.Company.Absences(r.Context(), from, to)
	if err != nil {
		return err
	}
	uid := r.URL.Query().Get("user_id")
	if uid == "me" {
		uid = p.UserID
	}
	out := make([]apiAbsenceOut, 0, len(list))
	for _, a := range list {
		if uid != "" && a.UserID != uid {
			continue
		}
		out = append(out, apiAbsenceOut{a.ID, a.UserID, a.UserName, a.Kind, a.From.Format(time.DateOnly), a.To.Format(time.DateOnly),
			a.Comment, a.ActingID, a.ActingName, a.Rights})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/absences {"user_id","kind","from","to","comment","acting_id","acting_rights"} — an absence: own, or
// (as on the page) of a person below the key owner, by an HR officer or an administrator.
func (s *Server) apiAbsenceAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		UserID   string   `json:"user_id"`
		Kind     string   `json:"kind"`
		From     string   `json:"from"`
		To       string   `json:"to"`
		Comment  string   `json:"comment"`
		ActingID string   `json:"acting_id"`
		Rights   []string `json:"acting_rights"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.UserID == "" || in.UserID == "me" {
		in.UserID = p.UserID
	}
	from, err1 := time.ParseInLocation(time.DateOnly, in.From, time.Local)
	to, err2 := time.ParseInLocation(time.DateOnly, in.To, time.Local)
	if err1 != nil || err2 != nil {
		return i18n.E("err.bad_form")
	}
	id, err := s.Company.CreateAbsence(r.Context(), p.UserID, in.UserID, in.Kind, from, to, in.Comment, s.apiAdmin(r.Context(), p.UserID),
		in.ActingID, in.Rights)
	if err != nil {
		return err
	}
	s.Log.Info("api absence added", "user_id", p.UserID, "id", id, "for", in.UserID)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
	return nil
}

// DELETE /api/v1/absences/{id} — with the rights of adding it.
func (s *Server) apiAbsenceDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Company.DeleteAbsence(r.Context(), p.UserID, r.PathValue("id"), s.apiAdmin(r.Context(), p.UserID)); err != nil {
		return err
	}
	s.Log.Info("api absence deleted", "user_id", p.UserID, "id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiInviteOut struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	DepartmentID string     `json:"department_id,omitempty"`
	Department   string     `json:"department,omitempty"`
	State        string     `json:"state"` // waiting, joined, expired, revoked
	Link         string     `json:"link,omitempty"`
	Mailed       bool       `json:"mailed"`
	Expires      time.Time  `json:"expires"`
	Created      time.Time  `json:"created"`
	Revoked      *time.Time `json:"revoked,omitempty"`
	InvitedBy    apiDeptRef `json:"invited_by"`
}

func apiInviteOf(i company.Invite, now time.Time) apiInviteOut {
	o := apiInviteOut{ID: i.ID, Email: i.Email, Name: i.Name, DepartmentID: i.DepartmentID, Department: i.Department,
		State: i.State(now), Mailed: i.Mailed, Expires: i.Expires, Created: i.Created, Revoked: i.Revoked,
		InvitedBy: apiDeptRef{i.InvitedByID, i.InvitedBy}}
	if o.State == "waiting" {
		o.Link = i.Link
	}
	return o
}

// apiInviter: the key owner may invite employees (as on the page) — and their name for the letter.
func (s *Server) apiInviter(r *http.Request, p apikeys.Principal) (string, error) {
	ok, err := s.Company.CanInvite(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", i18n.E("err.forbidden")
	}
	me, err := s.Company.Profile(r.Context(), p.UserID)
	return me.Name, err
}

// POST /api/v1/users/invite {"people":[{"email","name"}], "department_id"} — invites them (user.add): the sign-up link
// by e-mail from the key owner's mailbox, also returned to pass on.
func (s *Server) apiInvite(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	by, err := s.apiInviter(r, p)
	if err != nil {
		return err
	}
	var in struct {
		People []struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		} `json:"people"`
		DepartmentID string `json:"department_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var lines []string
	for _, x := range in.People {
		lines = append(lines, strings.ReplaceAll(x.Email+" "+x.Name, "\n", " "))
	}
	people, err := s.Company.ParseInvitees(r.Context(), strings.Join(lines, "\n"))
	if err == nil {
		err = s.Company.CheckDepartment(r.Context(), in.DepartmentID)
	}
	if err != nil {
		return err
	}
	sent, _, err := s.invite(r, p.UserID, by, in.DepartmentID, people)
	if err != nil && len(sent) == 0 {
		return err
	}
	if err != nil {
		s.Log.Error("api authentik invite", "err", err)
	}
	s.Log.Info("api employees invited", "user_id", p.UserID, "count", len(sent))
	out := []apiInviteOut{}
	for _, i := range sent {
		if kept, e := s.Company.InviteByID(r.Context(), i.ID); e == nil { // with the department's name
			i = kept
		}
		out = append(out, apiInviteOf(i, time.Now()))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invites": out})
	return nil
}

// GET /api/v1/users/invites — the latest invitations.
func (s *Server) apiInvites(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiInviter(r, p); err != nil {
		return err
	}
	list, err := s.Company.Invites(r.Context())
	if err != nil {
		return err
	}
	out := []apiInviteOut{}
	for _, i := range list {
		out = append(out, apiInviteOf(i, time.Now()))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/users/invites/{id}/resend — «Пригласить повторно».
func (s *Server) apiInviteResend(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	by, err := s.apiInviter(r, p)
	if err != nil {
		return err
	}
	inv, _, err := s.reinvite(r, p.UserID, by, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiInviteOf(inv, time.Now()))
	return nil
}

// DELETE /api/v1/users/invites/{id} — cancels the invitation.
func (s *Server) apiInviteCancel(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiInviter(r, p); err != nil {
		return err
	}
	if _, err := s.uninvite(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---------- teams ----------

type apiTeamOut struct {
	ID           string         `json:"id"`
	DepartmentID string         `json:"department_id"`
	Department   string         `json:"department"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	HeadID       string         `json:"head_id,omitempty"`
	HeadName     string         `json:"head_name,omitempty"`
	Deputies     []apiPersonOut `json:"deputies"`
	Members      []apiPersonOut `json:"members"`
}

func apiTeamOf(t *company.Team) apiTeamOut {
	out := apiTeamOut{ID: t.ID, DepartmentID: t.DepartmentID, Department: t.Department, Name: t.Name, Description: t.Description,
		Deputies: apiPeople(t.Deputies), Members: apiPeople(t.Members)}
	if t.Head != nil {
		out.HeadID, out.HeadName = t.Head.ID, t.Head.Name
	}
	return out
}

// GET /api/v1/teams?department_id= — the teams of the structure with their people.
func (s *Server) apiTeams(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Company.Teams(r.Context(), r.URL.Query().Get("department_id"))
	if err != nil {
		return err
	}
	out := make([]apiTeamOut, 0, len(list))
	for _, t := range list {
		out = append(out, apiTeamOf(t))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/teams/{id}
func (s *Server) apiTeam(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	t, err := s.Company.Team(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiTeamOf(t))
	return nil
}

type apiTeamIn struct {
	DepartmentID *string   `json:"department_id"`
	Name         *string   `json:"name"`
	Description  *string   `json:"description"`
	HeadID       *string   `json:"head_id"`
	Deputies     *[]string `json:"deputies"`
	Members      *[]string `json:"members"`
}

// apply puts the given fields over a team's input.
func (in apiTeamIn) apply(t *company.TeamInput) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&t.DepartmentID, in.DepartmentID)
	set(&t.Name, in.Name)
	set(&t.Description, in.Description)
	set(&t.HeadID, in.HeadID)
	if in.Deputies != nil {
		t.Deputies = *in.Deputies
	}
	if in.Members != nil {
		t.Members = *in.Members
	}
}

// POST /api/v1/teams {"department_id","name","description","head_id","deputies","members"} — a new team
// (administrators); without department_id — under the company.
func (s *Server) apiTeamAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	var in apiTeamIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var t company.TeamInput
	in.apply(&t)
	if t.DepartmentID == "" {
		root, _, err := s.apiTree(r)
		if err != nil {
			return err
		}
		t.DepartmentID = root.ID
	}
	id, err := s.Company.CreateTeam(r.Context(), t)
	if err != nil {
		return err
	}
	s.Log.Info("api team added", "user_id", p.UserID, "id", id)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
	return nil
}

// PATCH /api/v1/teams/{id} — changes the given fields: administrators all of them, the team's head — all but
// department_id and head_id.
func (s *Server) apiTeamUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	admin := s.apiAdmin(r.Context(), p.UserID)
	ok, err := s.Company.CanEditTeam(r.Context(), p.UserID, admin, r.PathValue("id"))
	if err != nil {
		return err
	}
	if !ok {
		return i18n.E("err.forbidden")
	}
	var in apiTeamIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	cur, err := s.Company.Team(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	t := company.TeamInput{DepartmentID: cur.DepartmentID, Name: cur.Name, Description: cur.Description}
	if cur.Head != nil {
		t.HeadID = cur.Head.ID
	}
	for _, x := range cur.Deputies {
		t.Deputies = append(t.Deputies, x.ID)
	}
	for _, x := range cur.Members {
		t.Members = append(t.Members, x.ID)
	}
	in.apply(&t)
	if err := s.Company.UpdateTeam(r.Context(), cur.ID, t, !admin); err != nil {
		return err
	}
	s.Log.Info("api team updated", "user_id", p.UserID, "id", cur.ID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// DELETE /api/v1/teams/{id} — administrators.
func (s *Server) apiTeamDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	if err := s.Company.DeleteTeam(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	s.Log.Info("api team deleted", "user_id", p.UserID, "id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiHonorOut struct {
	ID          int64      `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	Position    string     `json:"position"`
	Departments []string   `json:"departments"`
	Reason      string     `json:"reason"`
	From        string     `json:"from"`
	To          string     `json:"to,omitempty"` // none — until taken off
	By          apiDeptRef `json:"by"`
	Created     time.Time  `json:"created"`
}

func apiHonorOf(h company.Honor) apiHonorOut {
	o := apiHonorOut{ID: h.ID, UserID: h.Person.ID, Name: h.Person.Name, Position: h.Person.Position, Departments: h.Departments,
		Reason: h.Reason, From: h.From.Format(time.DateOnly), By: apiDeptRef{h.ByID, h.ByName}, Created: h.Created}
	if h.To != nil {
		o.To = h.To.Format(time.DateOnly)
	}
	return o
}

// GET /api/v1/honors?department_id=&user_id=&archive=1 — the honor board today (and those to come), or its archive.
func (s *Server) apiHonors(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	f := company.HonorFilter{DepartmentID: q.Get("department_id"), UserID: q.Get("user_id"), Archive: q.Get("archive") == "1"}
	if f.UserID == "me" {
		f.UserID = p.UserID
	}
	now := time.Now()
	list, err := s.Company.Honors(r.Context(), time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local), f)
	if err != nil {
		return err
	}
	out := make([]apiHonorOut, 0, len(list))
	for _, h := range list {
		out = append(out, apiHonorOf(h))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiHonorKeeper: the key owner keeps the honor board.
func (s *Server) apiHonorKeeper(r *http.Request, p apikeys.Principal) error {
	ok, err := s.Company.CanHonor(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID))
	if err == nil && !ok {
		err = i18n.E("err.forbidden")
	}
	return err
}

// POST /api/v1/honors {"user_id","reason","from","to"} — puts an employee on the board (from — today by default, no
// to — until taken off).
func (s *Server) apiHonorAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiHonorKeeper(r, p); err != nil {
		return err
	}
	var in struct {
		UserID string `json:"user_id"`
		Reason string `json:"reason"`
		From   string `json:"from"`
		To     string `json:"to"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if in.From != "" {
		var err error
		if from, err = time.ParseInLocation(time.DateOnly, in.From, time.Local); err != nil {
			return i18n.E("err.bad_form")
		}
	}
	var to *time.Time
	if in.To != "" {
		t, err := time.ParseInLocation(time.DateOnly, in.To, time.Local)
		if err != nil {
			return i18n.E("err.bad_form")
		}
		to = &t
	}
	id, err := s.Company.AddHonor(r.Context(), p.UserID, in.UserID, in.Reason, from, to)
	if err != nil {
		return err
	}
	h, err := s.Company.Honor(r.Context(), id)
	if err != nil {
		return err
	}
	s.Log.Info("api honor added", "user_id", p.UserID, "id", id, "for", in.UserID)
	writeJSON(w, http.StatusCreated, apiHonorOf(h))
	return nil
}

// DELETE /api/v1/honors/{id} — takes the person off the board.
func (s *Server) apiHonorDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiHonorKeeper(r, p); err != nil {
		return err
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Company.DeleteHonor(r.Context(), id); err != nil {
		return err
	}
	s.Log.Info("api honor deleted", "user_id", p.UserID, "id", id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
