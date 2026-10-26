package web

import (
	"net/http"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/tasks"
)

// The people picker's server side (static/js/people.js, template "upick"), also the @-mention list:
//   GET /people?q=&recent=1&dept=&deep=1&ids=a,b&im=1&self=1&collab=KEY&limit=&offset=
//   GET /people/departments — the company structure for its «Отделы» tab.

type personJSON struct {
	tasks.PersonHit
	Photo  string `json:"photo"`
	Active bool   `json:"active"`
}

func (s *Server) people(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	var ids []string
	if v := q.Get("ids"); v != "" {
		ids = strings.Split(v, ",")
	}
	list, err := s.Tasks.People(r.Context(), sess.UserID, tasks.PeopleQuery{Q: q.Get("q"), Recent: q.Get("recent") == "1",
		Department: q.Get("dept"), Deep: q.Get("deep") == "1", IDs: ids, Messenger: q.Get("im") == "1",
		Self: q.Get("self") == "1", Collab: q.Get("collab"), Limit: min(max(limit, 0), 1000), Offset: max(offset, 0)})
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := make([]personJSON, 0, len(list))
	for _, p := range list {
		out = append(out, personJSON{PersonHit: p, Photo: company.PhotoURL(p.ID, p.PhotoKey), Active: p.Active})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) peopleDepartments(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_, flat, err := s.Company.Tree(r.Context())
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	type dept struct {
		ID     string `json:"id"`
		Parent string `json:"parent"`
		Name   string `json:"name"`
		Depth  int    `json:"depth"`
		People int    `json:"people"` // with the sub-departments, everybody once
	}
	var count func(d *company.Department, seen map[string]bool)
	count = func(d *company.Department, seen map[string]bool) {
		for _, p := range d.Employees {
			seen[p.ID] = true
		}
		for _, c := range d.Children {
			count(c, seen)
		}
	}
	out := make([]dept, 0, len(flat))
	for _, d := range flat {
		seen := map[string]bool{}
		count(d, seen)
		out = append(out, dept{ID: d.ID, Parent: d.ParentID, Name: d.Name, Depth: d.Depth, People: len(seen)})
	}
	writeJSON(w, http.StatusOK, out)
}
