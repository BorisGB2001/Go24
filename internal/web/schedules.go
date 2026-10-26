package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Графики работы»: the list, the form of a schedule (an administrator changes it, everyone sees it) and
// the «График смен» of a shift schedule — its people × the days of a month; administrators and the managers of a
// person plan the person's shifts.

// workSchedules: GET /worktime/schedules.
func (s *Server) workSchedules(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Company.WorkSchedules(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	s.render(w, r, "wschedules", page{TitleKey: "wschedule.title", Nav: "wschedules", Data: map[string]any{
		"List": list, "Admin": sess.Admin,
	}})
}

// memberChips are the chosen members of a schedule for the people picker: a department as «🏢 name», as people.js does.
func memberChips(refs []string, names map[string]string) []tasks.User {
	var out []tasks.User
	for _, ref := range refs {
		name := names[ref]
		if strings.HasPrefix(ref, "dept:") {
			name = "🏢 " + name
		}
		out = append(out, tasks.User{ID: ref, Name: name})
	}
	return out
}

// workSchedulePage: GET /worktime/schedules/new and /worktime/schedules/{id}.
func (s *Server) workSchedulePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ws := &company.WorkSchedule{Kind: "fixed", Times: []company.ScheduleTime{{Days: []int{1, 2, 3, 4, 5}, Start: "09:00", End: "18:00", Break: 60}},
		MemberNames: map[string]string{}}
	if id := r.PathValue("id"); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			s.taskErr(w, r, i18n.E("err.not_found"), "/worktime/schedules")
			return
		}
		if ws, err = s.Company.WorkSchedule(r.Context(), n); err != nil {
			s.taskErr(w, r, err, "/worktime/schedules")
			return
		}
	} else if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/worktime/schedules")
		return
	}
	times := append(slices.Clone(ws.Times), company.ScheduleTime{Start: "09:00", End: "18:00"}) // a free row to add days
	shifts := append(slices.Clone(ws.Shifts), company.Shift{}, company.Shift{})
	var people []company.Person
	if ws.ID != 0 {
		var err error
		if people, err = s.Company.SchedulePeople(r.Context(), ws.ID); err != nil {
			s.taskErr(w, r, err, "/worktime/schedules")
			return
		}
	}
	tol := ""
	if ws.Tolerance > 0 {
		tol = strconv.Itoa(ws.Tolerance)
	}
	title, titleKey := ws.Name, ""
	if ws.ID == 0 {
		titleKey = "wschedule.new"
	}
	s.render(w, r, "wschedule", page{Title: title, TitleKey: titleKey, Nav: "wschedules", Data: map[string]any{
		"WS": ws, "Times": times, "Shifts": shifts, "Kinds": company.ScheduleKinds, "Days": []int{1, 2, 3, 4, 5, 6, 0},
		"Members": memberChips(ws.Members, ws.MemberNames), "Excluded": memberChips(ws.Excluded, ws.MemberNames),
		"People": people, "Admin": sess.Admin, "Tolerance": tol,
	}})
}

// saveWorkSchedule: POST /worktime/schedules and /worktime/schedules/{id}; rows of days t{i}_day with t{i}_start,
// t{i}_end, t{i}_break, shifts as the lists sh_id, sh_name, sh_start, sh_end, sh_break.
func (s *Server) saveWorkSchedule(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/worktime/schedules"
	if id := r.PathValue("id"); id != "" {
		back += "/" + id
	} else {
		back += "/new"
	}
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	num := func(v string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	ws := company.WorkSchedule{Name: r.FormValue("name"), Kind: r.FormValue("kind"), LateAfter: r.FormValue("late_after"),
		LeaveBefore: r.FormValue("leave_before"), MinDuration: r.FormValue("min_duration"), Tolerance: num(r.FormValue("tolerance")),
		Members: r.Form["members"], Excluded: r.Form["excluded"]}
	if id := r.PathValue("id"); id != "" {
		ws.ID, _ = strconv.ParseInt(id, 10, 64)
	}
	for i := 0; i < 8; i++ {
		p := "t" + strconv.Itoa(i) + "_"
		var days []int
		for _, d := range r.Form[p+"day"] {
			days = append(days, num(d))
		}
		if len(days) > 0 {
			ws.Times = append(ws.Times, company.ScheduleTime{Days: days, Start: r.FormValue(p + "start"), End: r.FormValue(p + "end"),
				Break: num(r.FormValue(p + "break"))})
		}
	}
	at := func(list []string, i int) string {
		if i < len(list) {
			return list[i]
		}
		return ""
	}
	for i, name := range r.Form["sh_name"] {
		id, _ := strconv.ParseInt(at(r.Form["sh_id"], i), 10, 64)
		ws.Shifts = append(ws.Shifts, company.Shift{ID: id, Name: name, Start: at(r.Form["sh_start"], i), End: at(r.Form["sh_end"], i),
			Break: num(at(r.Form["sh_break"], i))})
	}
	if err := s.Company.SaveWorkSchedule(r.Context(), sess.UserID, &ws); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("work schedule saved", "user_id", sess.UserID, "id", ws.ID, "kind", ws.Kind)
	http.Redirect(w, r, "/worktime/schedules/"+strconv.FormatInt(ws.ID, 10)+"?msg="+url.QueryEscape(s.tr(r, "wschedule.saved")), http.StatusSeeOther)
}

// deleteWorkSchedule: POST /worktime/schedules/{id}/delete.
func (s *Server) deleteWorkSchedule(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/worktime/schedules")
		return
	}
	if err := s.Company.DeleteWorkSchedule(r.Context(), id); err != nil {
		s.taskErr(w, r, err, "/worktime/schedules")
		return
	}
	s.Log.Info("work schedule deleted", "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, "/worktime/schedules?msg="+url.QueryEscape(s.tr(r, "wschedule.deleted")), http.StatusSeeOther)
}

// shiftPlanData: the shift schedule, its people with those the user may plan (an administrator — all, a manager —
// the people below them) and the month.
func (s *Server) shiftPlanData(r *http.Request, sess auth.Session) (*company.WorkSchedule, []company.Person, map[string]bool, time.Time, error) {
	month := sheetMonth(r, time.Now())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, nil, nil, month, i18n.E("err.not_found")
	}
	ws, err := s.Company.WorkSchedule(r.Context(), id)
	if err != nil {
		return nil, nil, nil, month, err
	}
	if ws.Kind != "shift" {
		return nil, nil, nil, month, i18n.E("err.not_found")
	}
	people, err := s.Company.SchedulePeople(r.Context(), id)
	if err != nil {
		return nil, nil, nil, month, err
	}
	can := map[string]bool{}
	for _, p := range people {
		ok, err := s.Company.Manages(r.Context(), sess.UserID, p.ID, sess.Admin)
		if err != nil {
			return nil, nil, nil, month, err
		}
		can[p.ID] = ok
	}
	return ws, people, can, month, nil
}

// shiftPlan: GET /worktime/schedules/{id}/plan?month=2026-10 — «График смен».
func (s *Server) shiftPlan(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ws, people, can, month, err := s.shiftPlanData(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/schedules")
		return
	}
	ids := make([]string, len(people))
	for i, p := range people {
		ids[i] = p.ID
	}
	end := month.AddDate(0, 1, 0)
	plan, err := s.Company.ShiftPlan(r.Context(), ids, month, end)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/schedules")
		return
	}
	absences, err := s.Company.Absences(r.Context(), month, end.AddDate(0, 0, -1))
	if err != nil {
		s.taskErr(w, r, err, "/worktime/schedules")
		return
	}
	absent := map[string]string{}
	for _, a := range absences {
		for t := a.From; !t.After(a.To); t = t.AddDate(0, 0, 1) {
			absent[a.UserID+"|"+t.Format(time.DateOnly)] = a.Kind
		}
	}
	var dates []time.Time
	for t := month; t.Before(end); t = t.AddDate(0, 0, 1) {
		dates = append(dates, t)
	}
	editable := false
	for _, ok := range can {
		editable = editable || ok
	}
	counts := map[int64]int{}
	for _, sid := range plan {
		counts[sid]++
	}
	s.render(w, r, "shiftplan", page{Title: ws.Name, Nav: "wschedules", Data: map[string]any{
		"WS": ws, "People": people, "Can": can, "Editable": editable, "Plan": plan, "Absent": absent, "Dates": dates,
		"Month": month, "Today": time.Now().Format(time.DateOnly), "Counts": counts,
		"Prev": month.AddDate(0, -1, 0).Format("2006-01"), "Next": month.AddDate(0, 1, 0).Format("2006-01"),
	}})
}

// saveShiftPlan: POST /worktime/schedules/{id}/plan?month= — cells c|{user}|{day} = a shift id or "".
func (s *Server) saveShiftPlan(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ws, people, can, month, err := s.shiftPlanData(r, sess)
	back := "/worktime/schedules/" + r.PathValue("id") + "/plan?month=" + month.Format("2006-01")
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	var mine []string
	for _, p := range people {
		if can[p.ID] {
			mine = append(mine, p.ID)
		}
	}
	if len(mine) == 0 {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	cells := map[string]int64{}
	for k, v := range r.Form {
		key, ok := strings.CutPrefix(k, "c|")
		if !ok || len(v) == 0 || v[0] == "" {
			continue
		}
		if id, err := strconv.ParseInt(v[0], 10, 64); err == nil {
			cells[key] = id
		}
	}
	if err := s.Company.SetShiftPlan(r.Context(), ws.ID, mine, month, month.AddDate(0, 1, 0), cells); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("shift plan saved", "user_id", sess.UserID, "schedule", ws.ID, "month", month.Format("2006-01"), "cells", len(cells))
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(s.tr(r, "wschedule.plan_saved")), http.StatusSeeOther)
}
