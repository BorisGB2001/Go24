package web

import (
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Файлы» and «Комментарии» of a meeting, as on a meeting's page ----------

// eventForm reads a form of the meeting's page, with its uploaded files when it is multipart.
func eventForm(w http.ResponseWriter, r *http.Request) ([]*multipart.FileHeader, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return nil, i18n.E("err.file_too_big")
		}
		return r.MultipartForm.File["file"], nil
	}
	return nil, r.ParseForm()
}

// eventAttach attaches the uploaded files and those of the drives (their ids) to the meeting or its comment (nf).
func (s *Server) eventAttach(r *http.Request, sess auth.Session, nf storages.NewFile, files []*multipart.FileHeader, drives []string) error {
	if err := s.attachPostFiles(r, sess, nf, files); err != nil {
		return err
	}
	for _, id := range drives {
		if _, err := s.attachDriveFile(r, sess, id, nf); err != nil {
			return err
		}
	}
	return nil
}

// eventFiles attaches files to the meeting: uploaded (file) or «Файл с Диска» (file=its id); its organizer does.
func (s *Server) eventFiles(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/calendar/" + id + "#files"
	files, err := eventForm(w, r)
	var e calendar.Event
	if err == nil {
		e, err = s.Calendar.Seen(r.Context(), sess.UserID, id)
	}
	if err == nil && e.OwnerID != sess.UserID && !tasks.IsAdmin(r.Context()) {
		err = calendar.ErrForbidden
	}
	var drives []string
	if r.MultipartForm == nil {
		drives = r.Form["file"]
	}
	if err == nil && len(files)+len(drives) == 0 {
		err = i18n.E("err.file_required")
	}
	if err == nil && len(files)+len(drives) > maxPostFiles {
		err = i18n.E("err.too_many_files", maxPostFiles)
	}
	if err == nil {
		err = s.eventAttach(r, sess, storages.NewFile{EventID: e.ID}, files, drives)
	}
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// eventFileRemove takes a file off the meeting.
func (s *Server) eventFileRemove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/calendar/" + id + "#files"
	_, err := s.Calendar.Seen(r.Context(), sess.UserID, id)
	if err == nil {
		err = s.Calendar.RemoveFile(r.Context(), sess.UserID, id, r.PathValue("file"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// eventComment adds a comment (body, with files: uploaded and drive_file) to the meeting, or deletes one (delete: its id).
func (s *Server) eventComment(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/calendar/" + id + "#comments"
	files, err := eventForm(w, r)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if c := r.FormValue("delete"); c != "" {
		if err = s.Calendar.DeleteComment(r.Context(), sess.UserID, id, c); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	drives := r.Form["drive_file"]
	if len(files)+len(drives) > maxPostFiles {
		s.taskErr(w, r, i18n.E("err.too_many_files", maxPostFiles), back)
		return
	}
	var names []string
	for _, h := range files {
		names = append(names, path.Base(strings.ReplaceAll(h.Filename, "\\", "/")))
	}
	for _, f := range drives {
		m, err := s.fileAccess(r, sess, f)
		if err != nil { // no file the author may not read
			s.taskErr(w, r, err, back)
			return
		}
		names = append(names, m.Name)
	}
	c, err := s.Calendar.AddComment(r.Context(), sess.UserID, id, r.FormValue("body"), names)
	if err == nil {
		back = "/calendar/" + id + "#c-" + c
		err = s.eventAttach(r, sess, storages.NewFile{EventID: id, EventCommentID: c}, files, drives)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
