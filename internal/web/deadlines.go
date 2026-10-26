package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// deadlineColumn is a column of «Сроки».
type deadlineColumn struct {
	Period string
	Tasks  []tasks.Task
}

// deadlines: the person's open tasks in a role (as on «Мои задачи») by deadline.
func (s *Server) deadlines(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	role := r.URL.Query().Get("role")
	if !slices.Contains(myRoles, role) {
		role = "responsible"
	}
	list, err := s.Tasks.ListTasks(r.Context(), tasks.Filter{UserID: sess.UserID, Role: role, OnlyOpen: true})
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	now := time.Now()
	cols := make([]deadlineColumn, len(tasks.DeadlinePeriods))
	index := map[string]int{}
	for i, p := range tasks.DeadlinePeriods {
		cols[i].Period, index[p] = p, i
	}
	for _, t := range list {
		i := index[tasks.DeadlinePeriod(t.Deadline, now)]
		cols[i].Tasks = append(cols[i].Tasks, t)
	}
	for _, c := range cols {
		slices.SortStableFunc(c.Tasks, func(a, b tasks.Task) int {
			if a.Deadline == nil || b.Deadline == nil {
				return 0
			}
			return a.Deadline.Compare(*b.Deadline)
		})
	}
	s.render(w, r, "deadlines", page{TitleKey: "title.deadlines", Nav: "deadlines", Data: map[string]any{
		"Role": role, "Roles": myRoles, "Columns": cols}})
}

// deadlineMove is drag & drop on «Сроки» (fetch): {deadline: "the new deadline as text" | ""} or an error.
func (s *Server) deadlineMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.FormValue("task"))
	var d *time.Time
	if err == nil {
		d, err = s.Tasks.MoveToPeriod(r.Context(), sess.UserID, id, r.FormValue("period"), time.Now())
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	text := ""
	if d != nil {
		text = d.Local().Format(s.tr(r, "format.datetime"))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"deadline": text})
}
