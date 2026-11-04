package web

import (
	"net/http"
	"path"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- files of a checklist item ----------

// checklistFileTask is the task of the card and whether the user may attach files to its items: who sees the task
// and edits its checklists (ChecklistItemFiles checks that), a collab's guest — when guests upload files.
func (s *Server) checklistFileTask(r *http.Request, sess auth.Session) (string, error) {
	taskID, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err == nil {
		err = s.Tasks.CheckVisible(r.Context(), sess.UserID, taskID)
	}
	if err == nil {
		err = s.Tasks.CanAttach(r.Context(), sess.UserID, taskID)
	}
	return taskID, err
}

// checklistFileUpload attaches a file from the computer to the item (POST /t/{key}/checklist/{id}/files, "file").
func (s *Server) checklistFileUpload(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	back, item := "/t/"+r.PathValue("key")+"#checklist", r.PathValue("id")
	taskID, err := s.checklistFileTask(r, sess)
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	var fileID string
	err = s.Tasks.ChecklistItemFiles(r.Context(), sess.UserID, taskID, item, true, func() (string, error) {
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return "", i18n.E("err.file_required")
		}
		defer f.Close()
		ct := hdr.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		name := path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/"))
		key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
		storageID := r.FormValue("storage")
		if tasks.IsGuestCtx(r.Context()) {
			storageID = "" // a guest stores in the built-in storage only
		}
		if err := s.Storages.Put(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), storageID, key, f, hdr.Size, ct); err != nil {
			return "", err
		}
		fileID, err = s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: ct, Size: hdr.Size,
			StorageID: storageID, TaskID: taskID, ChecklistItemID: item, UploadedBy: sess.UserID})
		return name, err
	})
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	s.keepUploaded(r, sess, fileID)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// checklistDriveFile is «Файл с Диска» of an item (POST /t/{key}/checklist/{id}/files/drive, "file" — the drive's
// file): a copy goes into the item.
func (s *Server) checklistDriveFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back, item := "/t/"+r.PathValue("key")+"#checklist", r.PathValue("id")
	taskID, err := s.checklistFileTask(r, sess)
	if err == nil {
		err = s.Tasks.ChecklistItemFiles(r.Context(), sess.UserID, taskID, item, true, func() (string, error) {
			return s.attachDriveFile(r, sess, r.FormValue("file"), storages.NewFile{TaskID: taskID, ChecklistItemID: item})
		})
	}
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// checklistFileDetach takes a file out of the item (POST /t/{key}/checklist/{id}/files/{file}/delete).
func (s *Server) checklistFileDetach(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	item := r.PathValue("id")
	s.taskPost(w, r, "#checklist", func(taskID string) error {
		return s.Tasks.ChecklistItemFiles(r.Context(), sess.UserID, taskID, item, false, func() (string, error) {
			return s.Storages.DetachChecklistFile(r.Context(), item, r.PathValue("file"))
		})
	})
}
