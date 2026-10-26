package web

import (
	"net/http"
	"regexp"
	"slices"

	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/drive"
	"go24/internal/tasks"
)

// fileCard is the card of a file of a drive («Подробнее», as a file's page of the Диск): what it is, where it lies,
// who uploaded and changed it, its versions and «Обсуждение» — for those who may read the file.
func (s *Server) fileCard(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	m, err := s.fileAccess(r, sess, id)
	if err == nil && m.Drive == "" {
		err = drive.ErrNotFound
	}
	var c drive.Card
	if err == nil {
		c, err = s.Drive.FileCard(r.Context(), id)
	}
	var versions []drive.Version
	if err == nil {
		versions, err = s.Drive.Versions(r.Context(), id)
	}
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	// the folder it lies in, as the viewer reaches it; someone else's «Мой диск» is reached by «Общие со мной» (an administrator's — by «Диск сотрудника»)
	at := ""
	switch {
	case c.Drive == "company":
		at = "/disk/company"
	case c.Drive == "project" && c.ProjectKey != "":
		at = "/disk/p/" + c.ProjectKey
	case c.Drive == "user" && c.DriveOwner == sess.UserID:
		at = "/disk/my"
	case c.Drive == "user" && tasks.IsAdmin(r.Context()): // an administrator reaches anyone's drive
		at = "/disk/u/" + c.DriveOwner
	}
	if at != "" && c.FolderID != "" {
		at += "?folder=" + c.FolderID
	}
	// «Общий доступ»: who sees the file and by what; the drive's own people change it here — the owner of «Мой диск»
	// opens the file to others, those who write in the drive make its public link
	sh, err := s.Drive.Sharing(r.Context(), id)
	var lim drive.Limits
	if err == nil {
		lim, err = s.Drive.Limits(r.Context())
	}
	var dr drive.Drive // the drive as the viewer has it, when it is theirs to change
	var users []tasks.User
	var depts []drive.Department
	if which := cardDrive(c, sess.UserID); err == nil && which != "" && !tasks.IsGuestCtx(r.Context()) {
		if dr, err = s.Drive.Open(r.Context(), sess.UserID, which); err != nil {
			dr, err = drive.Drive{}, nil
		}
		if dr.Kind == "user" && !dr.Employee {
			if users, err = s.Tasks.ListUsers(r.Context()); err == nil {
				depts, err = s.Drive.Departments(r.Context())
			}
			users = slices.DeleteFunc(users, func(u tasks.User) bool { return u.ID == sess.UserID || u.Guest })
		}
	}
	// «Бизнес-процессы» of a document of «Общий диск»: those run on it, the templates of files the viewer starts
	var bpTpls []bp.Template
	var bpRuns []bp.Instance
	bpWrite := false
	if err == nil && s.BP != nil && c.Drive == "company" {
		if bpTpls, err = s.BP.CardTemplates(r.Context(), bp.EntityFile, id); err == nil {
			bpTpls, err = s.BP.Startable(bpAdmin(r, sess), sess.UserID, bpTpls)
		}
		if err == nil {
			bpRuns, err = s.BP.Instances(r.Context(), sess.UserID, bp.EntityFile, id)
		}
		bpWrite = s.Drive.BPRight(r.Context(), sess.UserID, "write", id) == nil
	}
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	s.render(w, r, "diskcard", page{Title: c.Name, Nav: "disk", Data: map[string]any{
		"Card": c, "Type": fileType(c.Name, c.ContentType), "Versions": versions, "At": at, "Me": sess.UserID, "Admin": tasks.IsAdmin(r.Context()),
		"Sharing": sh, "Drive": dr, "Users": users, "Departments": depts, "FileAccesses": drive.FileAccesses,
		"LinksOff": lim.NoLinks, "Office": s.Office != nil, "Base": s.BaseURL, "Back": "/files/" + id + "/about#sharing",
		"BPTemplates": bpTpls, "BPInstances": bpRuns, "BPWrite": bpWrite}})
}

// cardDrive is the drive of the file the viewer changes its «Общий доступ» in ("" — none): their own «Мой диск»,
// the company's, a project's, an employee's for an administrator.
func cardDrive(c drive.Card, me string) string {
	switch {
	case c.Drive == "company":
		return "company"
	case c.Drive == "project" && c.ProjectKey != "":
		return c.ProjectKey
	case c.Drive == "user" && c.DriveOwner == me:
		return "my"
	case c.Drive == "user":
		return "u:" + c.DriveOwner
	}
	return ""
}

// cardBack is the file's card when the form came from it (back), else def.
func cardBack(r *http.Request, def string) string {
	if b := r.FormValue("back"); reCardBack.MatchString(b) {
		return b
	}
	return def
}

var reCardBack = regexp.MustCompile(`^/files/[0-9a-f-]{36}/about(#sharing)?$`)

// fileComment adds a comment (body) to «Обсуждение» of a file of a drive, or deletes one (delete: its id).
func (s *Server) fileComment(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/files/" + id + "/about"
	m, err := s.fileAccess(r, sess, id)
	if err == nil && m.Drive == "" {
		err = drive.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	if c := r.FormValue("delete"); c != "" {
		err = s.Drive.DeleteComment(r.Context(), sess.UserID, id, c)
	} else {
		var c string
		if c, err = s.Drive.AddComment(r.Context(), sess.UserID, id, r.FormValue("body")); err == nil {
			back += "#c-" + c
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
