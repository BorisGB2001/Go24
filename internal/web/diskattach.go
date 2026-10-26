package web

import (
	"net/http"
	"path"
	"time"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/storages"
)

// ---------- «Файл с Диска»: drive files attached to tasks and posts ----------

// attachDriveFile puts a copy of a drive's file the user may read into a task or a post (nf says which): later
// changes on the drive do not change what was attached. It is the file's name.
func (s *Server) attachDriveFile(r *http.Request, sess auth.Session, id string, nf storages.NewFile) (string, error) {
	if !reFileID.MatchString(id) {
		return "", drive.ErrNotFound
	}
	m, err := s.fileAccess(r, sess, id)
	if err == nil && m.Drive == "" {
		err = drive.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), m.Name)
	if err := s.Storages.Copy(r.Context(), m.StorageID, m.Key, key, m.Size, m.ContentType); err != nil {
		return "", err
	}
	nf.Key, nf.Name, nf.ContentType, nf.Size, nf.UploadedBy = key, m.Name, m.ContentType, m.Size, sess.UserID
	if m.StorageID != nil {
		nf.StorageID = *m.StorageID
	}
	_, err = s.Storages.AddFile(r.Context(), nf)
	return m.Name, err
}

// taskDriveFile is «Файл с Диска» of a task card.
func (s *Server) taskDriveFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	key := r.PathValue("key")
	back := "/t/" + key + "#files"
	taskID, err := s.Tasks.TaskIDByKey(r.Context(), key)
	if err == nil {
		err = s.Tasks.CheckVisible(r.Context(), sess.UserID, taskID)
	}
	if err == nil {
		err = s.Tasks.CanAttach(r.Context(), sess.UserID, taskID)
	}
	var name string
	if err == nil {
		name, err = s.attachDriveFile(r, sess, r.FormValue("file"), storages.NewFile{TaskID: taskID})
	}
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	if err := s.Tasks.FileAdded(r.Context(), sess.UserID, taskID, name); err != nil {
		s.Log.Error("file history", "err", err)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
