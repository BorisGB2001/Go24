package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Загруженные файлы» and «Сохранённые файлы» of «Мой диск» ----------

// ownFolder is a folder named so at the top of the person's own drive, made when missing.
func (s *Server) ownFolder(ctx context.Context, userID, name string) (drive.Drive, string, error) {
	d, err := s.Drive.Open(ctx, userID, "my")
	if err != nil {
		return d, "", err
	}
	folder, err := s.recordFolder(ctx, userID, d, name)
	return d, folder, err
}

// keepUploaded puts a copy of a file just attached from the computer (to a task, a post) into «Загруженные файлы» of
// the uploader's own drive. The attachment is kept whatever happens here: a full drive or a failure only skips the copy,
// and deleting the copy on the drive does not touch the attachment.
func (s *Server) keepUploaded(r *http.Request, sess auth.Session, fileID string) {
	if tasks.IsGuestCtx(r.Context()) {
		return // a guest of a collab has no drive
	}
	m, err := s.Storages.FileMeta(r.Context(), fileID)
	if err != nil {
		s.Log.Error("uploaded files: file", "err", err)
		return
	}
	d, err := s.Drive.Open(r.Context(), sess.UserID, "my")
	if err != nil {
		s.Log.Error("uploaded files: drive", "err", err)
		return
	}
	if s.Drive.Fits(r.Context(), d, m.Size) != nil {
		return
	}
	d, folder, err := s.ownFolder(r.Context(), sess.UserID, s.tr(r, "disk.uploaded_folder"))
	if err == nil {
		err = s.copyFile(r, sess, m, storages.NewFile{Drive: d.Kind, DriveOwner: d.OwnerID, FolderID: d.At(folder)})
	}
	if err != nil {
		s.Log.Error("uploaded files: copy", "err", err)
	}
}

// saveToDrive is «Сохранить на Диск» of an attachment (of a task, a post, a chat's drive file): a copy goes into
// «Сохранённые файлы» of the user's own drive.
func (s *Server) saveToDrive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/disk/my"
	}
	id := r.PathValue("id")
	if !reFileID.MatchString(id) {
		s.diskErr(w, r, drive.ErrNotFound, back)
		return
	}
	if tasks.IsGuestCtx(r.Context()) {
		s.diskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	m, err := s.fileAccess(r, sess, id)
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	d, err := s.Drive.Open(r.Context(), sess.UserID, "my")
	if err == nil {
		err = s.Drive.Fits(r.Context(), d, m.Size) // the drive's limit
	}
	var folder string
	if err == nil {
		d, folder, err = s.ownFolder(r.Context(), sess.UserID, s.tr(r, "im.drive_folder")) // the folder the messenger’s «Сохранить на Диск» fills too
	}
	if err == nil {
		err = s.copyFile(r, sess, m, storages.NewFile{Drive: d.Kind, DriveOwner: d.OwnerID, FolderID: d.At(folder)})
	}
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	to := d.Path() + "?folder=" + url.QueryEscape(folder)
	text := s.tr(r, "disk.saved_to", m.Name)
	if fromForm(r) || wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"url": to, "text": text})
		return
	}
	frag := ""
	if i := strings.Index(back, "#"); i >= 0 { // the message goes before the fragment
		back, frag = back[:i], back[i:]
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(text)+frag, http.StatusSeeOther)
}
