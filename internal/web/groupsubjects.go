package web

import (
	"net/http"
	"strconv"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/tasks"
)

// «Тематики групп» (GO24DEV-811): the administrator keeps the list; a group picks one and has «Ключевые слова».

func (s *Server) groupSubjects(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !tasks.IsAdmin(r.Context()) {
		s.taskErr(w, r, tasks.ErrForbidden, "/projects")
		return
	}
	list, err := s.Tasks.Subjects(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	s.render(w, r, "groupsubjects", page{TitleKey: "subjects.title", Nav: "projects", Data: list})
}

// groupSubjectSave adds, renames or (op=delete) removes a subject.
func (s *Server) groupSubjectSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	const back = "/projects/subjects"
	var err error
	if r.FormValue("op") == "delete" {
		err = s.Tasks.DeleteSubject(r.Context(), r.FormValue("id"))
	} else {
		sort, _ := strconv.Atoi(r.FormValue("sort"))
		_, err = s.Tasks.SaveSubject(r.Context(), r.FormValue("id"), r.FormValue("name"), sort)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("group subject saved", "user_id", sess.UserID, "op", r.FormValue("op"), "id", r.FormValue("id"))
	http.Redirect(w, r, back, http.StatusSeeOther)
}

type apiSubject struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Sort   int    `json:"sort"`
	Groups int    `json:"groups"`
}

func (s *Server) apiGroupSubjects(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Tasks.Subjects(r.Context())
	if err != nil {
		return err
	}
	out := make([]apiSubject, 0, len(list))
	for _, g := range list {
		out = append(out, apiSubject(g))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiGroupSubjectSave: POST adds a subject, PATCH /{id} renames it (the administrator).
func (s *Server) apiGroupSubjectSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name string `json:"name"`
		Sort int    `json:"sort"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.Tasks.SaveSubject(r.Context(), r.PathValue("id"), in.Name, in.Sort)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, apiSubject{ID: id, Name: in.Name, Sort: in.Sort})
	return nil
}

func (s *Server) apiGroupSubjectDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Tasks.DeleteSubject(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
