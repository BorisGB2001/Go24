package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST of joining groups (GO24DEV-774): «Все группы», the group's type, «Вступить» and requests to join.

func (s *Server) apiGroupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/groups", s.api("projects:read", s.apiGroups))
	mux.HandleFunc("GET /api/v1/groups/subjects", s.api("projects:read", s.apiGroupSubjects))
	mux.HandleFunc("POST /api/v1/groups/subjects", s.api("projects:write", s.apiGroupSubjectSave))
	mux.HandleFunc("PATCH /api/v1/groups/subjects/{id}", s.api("projects:write", s.apiGroupSubjectSave))
	mux.HandleFunc("DELETE /api/v1/groups/subjects/{id}", s.api("projects:write", s.apiGroupSubjectDelete))
	mux.HandleFunc("PATCH /api/v1/projects/{key}", s.api("projects:write", s.apiProjectPatch))
	mux.HandleFunc("POST /api/v1/projects/{key}/copy", s.api("projects:write", s.apiProjectCopy))
	mux.HandleFunc("POST /api/v1/projects/{key}/owner", s.api("projects:write", s.apiProjectOwner))
	mux.HandleFunc("DELETE /api/v1/projects/{key}", s.api("projects:write", s.apiProjectDelete))
	mux.HandleFunc("GET /api/v1/projects/{key}/tools", s.api("projects:read", s.apiGroupTools))
	mux.HandleFunc("PATCH /api/v1/projects/{key}/tools", s.api("projects:write", s.apiGroupTools))
	mux.HandleFunc("GET /api/v1/projects/{key}/departments", s.api("projects:read", s.apiGroupDepts))
	mux.HandleFunc("PUT /api/v1/projects/{key}/departments", s.api("projects:write", s.apiGroupDepts))
	mux.HandleFunc("POST /api/v1/projects/{key}/join", s.api("projects:write", s.apiGroupJoin))
	mux.HandleFunc("DELETE /api/v1/projects/{key}/join", s.api("projects:write", s.apiGroupJoinCancel))
	mux.HandleFunc("GET /api/v1/projects/{key}/join-requests", s.api("projects:read", s.apiGroupRequests))
	mux.HandleFunc("POST /api/v1/projects/{key}/join-requests/{uid}/{answer}", s.api("projects:write", s.apiGroupAnswer))
}

func (s *Server) apiGroups(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	list, err := s.Tasks.AllGroups(r.Context(), p.UserID, tasks.GroupFilter{Q: q.Get("q"), SubjectID: q.Get("subject_id"), Keyword: q.Get("keyword")})
	if err != nil {
		return err
	}
	type apiGroup struct {
		apiProject
		Members   int  `json:"members"`
		Requested bool `json:"requested"`
	}
	out := make([]apiGroup, 0, len(list))
	for _, g := range list {
		out = append(out, apiGroup{toAPIProject(g.Project), g.Members, g.Requested})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiProjectPatch changes the group's type, its subject and keywords, its rights to its tasks (the rights not given
// keep theirs).
func (s *Server) apiProjectPatch(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Privacy   string           `json:"privacy"`
		SubjectID *string          `json:"subject_id"`
		Keywords  *[]string        `json:"keywords"`
		TaskPerms *tasks.TaskPerms `json:"task_perms"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if in.Privacy != "" {
		if err := s.Tasks.SetPrivacy(r.Context(), pr, in.Privacy); err != nil {
			return err
		}
		pr.Privacy = in.Privacy
	}
	if in.TaskPerms != nil {
		tp, now := *in.TaskPerms, pr.TaskPerms()
		for _, x := range []struct{ v, was *string }{{&tp.Create, &now.Create}, {&tp.Edit, &now.Edit}, {&tp.Delete, &now.Delete}} {
			if *x.v == "" {
				*x.v = *x.was
			}
		}
		if err := s.Tasks.SetTaskPerms(r.Context(), pr, tp); err != nil {
			return err
		}
		pr.TaskCreate, pr.TaskEdit, pr.TaskDelete = tp.Create, tp.Edit, tp.Delete
	}
	if in.SubjectID != nil || in.Keywords != nil {
		subject, keywords := pr.SubjectID, pr.Keywords
		if in.SubjectID != nil {
			subject = *in.SubjectID
		}
		if in.Keywords != nil {
			keywords = tasks.ParseKeywords(strings.Join(*in.Keywords, ","))
		}
		if err := s.Tasks.SetGroupSubject(r.Context(), pr, subject, keywords); err != nil {
			return err
		}
		if pr, err = s.Tasks.ProjectByKey(r.Context(), p.UserID, pr.Key); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, toAPIProject(pr))
	return nil
}

// apiOutsider is the project of someone who may not be in it yet (ProjectByKey says ErrForbidden to an outsider).
func (s *Server) apiOutsider(r *http.Request, userID string) (tasks.Project, error) {
	pr, err := s.Tasks.ProjectByKey(r.Context(), userID, r.PathValue("key"))
	if errors.Is(err, tasks.ErrForbidden) {
		return pr, nil
	}
	if err == nil && pr.MyRole != "admin" {
		err = i18n.E("err.already_member")
	}
	return pr, err
}

func (s *Server) apiGroupJoin(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Message string `json:"message"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	pr, err := s.apiOutsider(r, p.UserID)
	if err != nil {
		return err
	}
	joined, err := s.Tasks.JoinGroup(r.Context(), pr, p.UserID, in.Message)
	if err != nil {
		return err
	}
	if joined {
		s.Matrix.Wake()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"joined": joined, "requested": !joined})
	return nil
}

func (s *Server) apiGroupJoinCancel(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil && !errors.Is(err, tasks.ErrForbidden) {
		return err
	}
	if err := s.Tasks.CancelJoinRequest(r.Context(), pr, p.UserID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) apiGroupRequests(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if !pr.CanManage() {
		return tasks.ErrForbidden
	}
	list, err := s.Tasks.JoinRequests(r.Context(), pr)
	if err != nil {
		return err
	}
	type apiRequest struct {
		User      apiUser   `json:"user"`
		Message   string    `json:"message"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := make([]apiRequest, 0, len(list))
	for _, j := range list {
		out = append(out, apiRequest{apiUser{j.UserID, j.UserName}, j.Message, j.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiGroupAnswer(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	answer := r.PathValue("answer")
	if answer != "accept" && answer != "reject" {
		return tasks.ErrNotFound
	}
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if err := s.Tasks.AnswerJoinRequest(r.Context(), pr, p.UserID, r.PathValue("uid"), answer == "accept"); err != nil {
		return err
	}
	if answer == "accept" {
		s.Matrix.Wake()
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiProjectOwner is POST /api/v1/projects/{key}/owner {user_id}: «Сменить владельца» (GO24DEV-926).
func (s *Server) apiProjectOwner(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		UserID string `json:"user_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if err := s.Tasks.TransferProject(r.Context(), pr, p.UserID, in.UserID); err != nil {
		return err
	}
	if pr, err = s.Tasks.ProjectByKey(r.Context(), p.UserID, pr.Key); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPIProject(pr))
	return nil
}

// apiProjectDelete is DELETE /api/v1/projects/{key}?confirm=name: «Удалить группу» (GO24DEV-926); a group with
// tasks is not deleted.
func (s *Server) apiProjectDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if err := s.Tasks.DeleteProject(r.Context(), pr, p.UserID, r.URL.Query().Get("confirm")); err != nil {
		return err
	}
	s.Log.Info("project deleted", "user_id", p.UserID, "project", pr.Key, "api", true)
	s.Matrix.Wake()
	w.WriteHeader(http.StatusNoContent)
	return nil
}
