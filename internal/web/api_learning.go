package web

import (
	"net/http"
	"time"

	"go24/internal/apikeys"
	"go24/internal/company"
	"go24/internal/i18n"
)

// REST of «Обучение» (the learning module), with the rights of the pages:
//
// GET    /api/v1/learning/courses?mine=1&q=             — the courses the key owner sees, with the owner's progress
// GET    /api/v1/learning/courses/{id}                  — a course with its lessons and tests
// GET    /api/v1/learning/assignments?course_id=&department_id=&status= — who learns what (keepers — everybody,
//                                                         heads — the people of their departments)
// POST   /api/v1/learning/assignments {"course_id","to":[user id | "dept:ID"],"due":"2026-11-30"}
// DELETE /api/v1/learning/assignments?course_id=&user_id=

func (s *Server) apiLearningRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/learning/courses", s.api("company:read", s.apiCourses))
	mux.HandleFunc("GET /api/v1/learning/courses/{id}", s.api("company:read", s.apiCourse))
	mux.HandleFunc("GET /api/v1/learning/assignments", s.api("company:read", s.apiLearners))
	mux.HandleFunc("POST /api/v1/learning/assignments", s.api("company:write", s.apiAssign))
	mux.HandleFunc("DELETE /api/v1/learning/assignments", s.api("company:write", s.apiUnassign))
}

type apiCourseOut struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Published   bool           `json:"published"`
	Author      string         `json:"author"`
	Lessons     int            `json:"lessons"`
	Done        int            `json:"done"`
	Finished    bool           `json:"finished"`
	Assigned    bool           `json:"assigned"`
	Due         string         `json:"due,omitempty"`
	Items       []apiLessonOut `json:"items,omitempty"`
}

type apiLessonOut struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Chapter  string `json:"chapter"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	PassPct  int    `json:"pass_pct,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
	Minutes  int    `json:"minutes,omitempty"`
	Done     bool   `json:"done"`
}

func apiDay(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.DateOnly)
}

func apiCourseOf(c company.Course) apiCourseOut {
	return apiCourseOut{ID: c.ID, Title: c.Title, Description: c.Description, Published: c.Published, Author: c.AuthorName,
		Lessons: c.Lessons, Done: c.Done, Finished: c.Finished(), Assigned: c.Assigned, Due: apiDay(c.Due)}
}

// apiLearnRights: the key owner keeps the courses (keep) and may assign them (assign).
func (s *Server) apiLearnRights(r *http.Request, p apikeys.Principal) (keep, assign bool, err error) {
	admin := s.apiAdmin(r.Context(), p.UserID)
	if keep, err = s.Company.CanTeach(r.Context(), p.UserID, admin); err != nil || keep {
		return keep, keep, err
	}
	assign, err = s.Company.CanAssign(r.Context(), p.UserID, admin)
	return false, assign, err
}

func (s *Server) apiCourses(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	keep, _, err := s.apiLearnRights(r, p)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	list, err := s.Company.Courses(r.Context(), p.UserID, keep, company.CourseFilter{Mine: q.Get("mine") == "1", Query: q.Get("q")})
	if err != nil {
		return err
	}
	out := make([]apiCourseOut, 0, len(list))
	for _, c := range list {
		out = append(out, apiCourseOf(c))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiCourse(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	keep, _, err := s.apiLearnRights(r, p)
	if err != nil {
		return err
	}
	c, err := s.Company.Course(r.Context(), r.PathValue("id"), p.UserID, keep)
	if err != nil {
		return err
	}
	lessons, err := s.Company.Lessons(r.Context(), c.ID, p.UserID)
	if err != nil {
		return err
	}
	out := apiCourseOf(c)
	out.Items = []apiLessonOut{}
	for _, l := range lessons {
		item := apiLessonOut{ID: l.ID, Kind: l.Kind, Chapter: l.Chapter, Title: l.Title, Body: l.Body, Done: l.Done}
		if l.Test() {
			item.PassPct, item.Attempts, item.Minutes = l.PassPct, l.Attempts, l.Minutes
		}
		out.Items = append(out.Items, item)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type apiLearnerOut struct {
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	CourseID string `json:"course_id"`
	Course   string `json:"course"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Finished bool   `json:"finished"`
	Overdue  bool   `json:"overdue"`
	Best     *int   `json:"best_test_pct"`
	Due      string `json:"due,omitempty"`
	Assigned string `json:"assigned"`
	By       string `json:"assigned_by"`
}

func (s *Server) apiLearners(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	keep, assign, err := s.apiLearnRights(r, p)
	if err != nil {
		return err
	}
	if !assign {
		return i18n.E("err.forbidden")
	}
	q := r.URL.Query()
	list, err := s.Company.Learners(r.Context(), p.UserID, keep, company.LearnerFilter{CourseID: q.Get("course_id"),
		DepartmentID: q.Get("department_id"), Status: q.Get("status")})
	if err != nil {
		return err
	}
	day := today()
	out := make([]apiLearnerOut, 0, len(list))
	for _, l := range list {
		out = append(out, apiLearnerOut{UserID: l.ID, Name: l.Name, CourseID: l.CourseID, Course: l.CourseTitle, Done: l.Done, Total: l.Total,
			Finished: l.Finished(), Overdue: l.Overdue(day), Best: l.Best, Due: apiDay(l.Due), Assigned: l.Assigned.Format(time.RFC3339), By: l.ByName})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiAssign(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	keep, assign, err := s.apiLearnRights(r, p)
	if err != nil {
		return err
	}
	if !assign {
		return i18n.E("err.forbidden")
	}
	var in struct {
		CourseID string   `json:"course_id"`
		To       []string `json:"to"`
		Due      string   `json:"due"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if len(in.To) == 0 {
		return i18n.E("learn.err_nobody")
	}
	var due *time.Time
	if in.Due != "" {
		t, err := time.ParseInLocation(time.DateOnly, in.Due, time.Local)
		if err != nil {
			return i18n.E("err.bad_form")
		}
		due = &t
	}
	n, err := s.Company.Assign(r.Context(), p.UserID, in.CourseID, in.To, due, keep)
	if err != nil {
		return err
	}
	s.Log.Info("api course assigned", "user_id", p.UserID, "id", in.CourseID, "people", n)
	writeJSON(w, http.StatusOK, map[string]int{"assigned": n})
	return nil
}

func (s *Server) apiUnassign(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	keep, assign, err := s.apiLearnRights(r, p)
	if err != nil {
		return err
	}
	if !assign {
		return i18n.E("err.forbidden")
	}
	q := r.URL.Query()
	if err := s.Company.Unassign(r.Context(), p.UserID, q.Get("course_id"), q.Get("user_id"), keep); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
