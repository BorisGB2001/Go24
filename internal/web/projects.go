package web

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
)

// The project's card: «О проекте» (name, description, type and dates), the avatar, the archive.

func (s *Server) projectInfo(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	day := func(name string) *time.Time {
		t, perr := time.ParseInLocation("2006-01-02", r.FormValue(name), time.Local)
		if perr != nil {
			return nil
		}
		return &t
	}
	if err == nil {
		err = s.Tasks.SaveProjectInfo(r.Context(), p, r.FormValue("name"), r.FormValue("description"), day("date_start"), day("date_finish"))
	}
	if err == nil && r.FormValue("kind") != "" && r.FormValue("kind") != p.Kind {
		err = s.Tasks.SetProjectKind(r.Context(), p, r.FormValue("kind"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Matrix.Wake() // the project room is named after the project
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) projectPhoto(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if r.FormValue("op") == "delete" {
		if _, err := s.Tasks.SetProjectPhoto(r.Context(), p, "", ""); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if !p.CanManage() {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoSize+1<<16)
	f, hdr, err := r.FormFile("photo")
	if err != nil || hdr.Size > maxPhotoSize {
		s.taskErr(w, r, i18n.E("err.photo"), back)
		return
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ct := http.DetectContentType(head[:n])
	ext, ok := photoTypes[ct]
	if !ok {
		s.taskErr(w, r, i18n.E("err.photo"), back)
		return
	}
	key := "go24/projects/" + p.ID + "/" + randomHex(8) + ext
	if err := s.Storages.Put(r.Context(), sess.UserID, false, "", key, io.MultiReader(bytes.NewReader(head[:n]), f), hdr.Size, ct); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if _, err := s.Tasks.SetProjectPhoto(r.Context(), p, key, ct); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("project photo uploaded", "user_id", sess.UserID, "project", p.Key)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// projectPhotoFile serves a project's avatar to signed-in users.
func (s *Server) projectPhotoFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	key, ct, err := s.Tasks.ProjectPhoto(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	obj, err := s.Storages.Open(r.Context(), nil, key)
	if err != nil {
		s.fail(w, "open project photo", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400") // the URL changes with every new avatar
	_, _ = io.Copy(w, obj)
}

// projectArchive moves the project to the archive (on=1) or back.
func (s *Server) projectArchive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.SetArchived(r.Context(), p, r.FormValue("on") == "1")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("project archive", "user_id", sess.UserID, "project", p.Key, "on", r.FormValue("on"))
	s.Matrix.Wake() // a collab's chat turns read-only (or back)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// inviteAnswer: the invited person accepts (accept=1) or declines an invitation.
func (s *Server) inviteAnswer(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	accept := r.FormValue("accept") == "1"
	if err := s.Tasks.AnswerInvite(r.Context(), sess.UserID, r.PathValue("key"), accept); err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	if accept {
		s.Matrix.Wake() // the new member joins the project room
		http.Redirect(w, r, "/p/"+r.PathValue("key"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/projects", http.StatusSeeOther)
}

// inviteCancel withdraws an invitation; invitePerm sets who may invite.
func (s *Server) inviteCancel(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.CancelInvite(r.Context(), p, r.PathValue("uid"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#members", http.StatusSeeOther)
}

func (s *Server) invitePerm(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.SetInvitePerm(r.Context(), p, r.FormValue("perm"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#members", http.StatusSeeOther)
}
