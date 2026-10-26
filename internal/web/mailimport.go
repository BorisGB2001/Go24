package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта»: «Импорт из файла» ----------

// As Outlook's «Импорт и экспорт → Импорт из другой программы или файла»: /mail/import takes a file — from the
// computer (it is kept in «Импорт почты» of the user's drive) or one of the drive — and asks where its letters go and
// what to do with duplicates; go24 worker puts them into the mailbox (RunMailImports) and notifies the user.

func (s *Server) mailImportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /mail/import", s.requireUser(s.mailImportPage))
	mux.HandleFunc("POST /mail/import", s.requireUser(s.mailImportAdd))
	mux.HandleFunc("POST /mail/import/{id}/delete", s.requireUser(s.mailImportDelete))
	mux.HandleFunc("GET /api/v1/mail/imports", s.api("mail:read", s.apiMailImports))
	mux.HandleFunc("POST /api/v1/mail/imports", s.api("mail:write", s.apiMailImportAdd))
	mux.HandleFunc("GET /api/v1/mail/imports/{id}", s.api("mail:read", s.apiMailImport))
	mux.HandleFunc("DELETE /api/v1/mail/imports/{id}", s.api("mail:write", s.apiMailImportDelete))
}

// importDriveFile is a file of the user's drive «Импорт из файла» may take.
type importDriveFile struct {
	ID, Name string
	Size     int64
}

// importDriveFiles are the files of the user's own drive that look like mail (.pst, .mbox, .zip, .eml), the latest first.
func (s *Server) importDriveFiles(ctx context.Context, userID string) ([]importDriveFile, error) {
	rows, err := s.DB.Query(ctx, `SELECT id::text, name, size FROM files WHERE drive = 'user' AND drive_owner::text = $1 AND deleted_at IS NULL
		AND lower(name) ~ '\.(pst|mbox|mbx|zip|eml)$' ORDER BY created_at DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []importDriveFile
	for rows.Next() {
		var f importDriveFile
		if err := rows.Scan(&f.ID, &f.Name, &f.Size); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Server) mailImportPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	boxes, err := s.Mail.Mailboxes(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	q := r.URL.Query()
	box := q.Get("box")
	if box == "" && len(boxes) > 0 {
		box = boxes[0].ID
	}
	folders, err := s.Mail.Folders(ctx, sess.UserID, box)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	imports, err := s.Mail.Imports(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	files, err := s.importDriveFiles(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	busy := false
	for _, e := range imports {
		busy = busy || e.Busy()
	}
	s.render(w, r, "mailimport", page{TitleKey: "mail.fimport.title", Nav: "mail", Data: map[string]any{
		"Boxes": boxes, "Box": box, "Folders": folders, "Folder": q.Get("folder"), "Files": files, "File": q.Get("file"),
		"Imports": imports, "Busy": busy, "Msg": q.Get("msg")}})
}

// mailImportAdd takes the page's form: its fields, then the file from the computer (the form keeps the file last),
// read as it comes — a .pst of gigabytes never sits in memory — into a temporary file, then onto the drive.
func (s *Server) mailImportAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, mail.MaxImportSize+1<<20)
	n := mail.NewImport{Lang: s.lang(r)}
	back := "/mail/import"
	fail := func(err error) {
		if n.MailboxID != "" {
			back += "?box=" + url.QueryEscape(n.MailboxID)
		}
		s.mailErr(w, r, err, back)
	}
	mr, err := r.MultipartReader()
	if err != nil {
		fail(i18n.E("err.bad_form"))
		return
	}
	var where, driveFile string
	var tmp *os.File
	defer func() {
		if tmp != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	var size int64
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(i18n.E("err.bad_form"))
			return
		}
		if p.FormName() == "file" && p.FileName() != "" && tmp == nil {
			if tmp, err = os.CreateTemp("", "go24-mail-import-*"); err != nil {
				fail(err)
				return
			}
			n.Name = path.Base(strings.ReplaceAll(p.FileName(), "\\", "/"))
			if size, err = io.Copy(tmp, p); err != nil {
				var big *http.MaxBytesError
				if errors.As(err, &big) {
					err = i18n.E("err.mail_import_big")
				}
				fail(err)
				return
			}
			continue
		}
		v, _ := io.ReadAll(io.LimitReader(p, 4096))
		switch p.FormName() {
		case "box":
			n.MailboxID = string(v)
		case "where":
			where = string(v)
		case "into":
			n.FolderID = string(v)
		case "dupes":
			n.Dupes = string(v) == "allow"
		case "drive_file":
			driveFile = string(v)
		}
	}
	if where != "folder" {
		n.FolderID = ""
	}
	switch {
	case tmp != nil && size > 0:
		head := make([]byte, 8)
		k, _ := tmp.ReadAt(head, 0)
		if n.Format = mail.ImportFormat(n.Name, head[:k]); n.Format == "" {
			fail(i18n.E("err.mail_import_format"))
			return
		}
		if _, err := s.Mail.Mailbox(r.Context(), sess.UserID, n.MailboxID); err != nil {
			fail(err)
			return
		}
		if _, err := tmp.Seek(0, 0); err != nil {
			fail(err)
			return
		}
		tr := func(key string, args ...any) string { return s.tr(r, key, args...) }
		if n.FileID, _, err = s.exportToDrive(r.Context(), sess.UserID, tr("mail.fimport.folder"), n.Name, "application/octet-stream", tmp, size); err != nil {
			fail(err)
			return
		}
	case driveFile != "":
		if err := s.importFromDrive(r.Context(), sess, driveFile, &n); err != nil {
			fail(err)
			return
		}
	default:
		fail(i18n.E("err.mail_import_file"))
		return
	}
	if _, err := s.Mail.AddImport(r.Context(), sess.UserID, n); err != nil {
		fail(err)
		return
	}
	s.Log.Info("mail import queued", "user_id", sess.UserID, "mailbox", n.MailboxID, "format", n.Format)
	http.Redirect(w, r, "/mail/import?box="+url.QueryEscape(n.MailboxID)+"&msg="+url.QueryEscape(s.tr(r, "mail.fimport.queued")), http.StatusSeeOther)
}

// importFromDrive fills the import with a file of the drive the user may read: its name and its kind.
func (s *Server) importFromDrive(ctx context.Context, sess auth.Session, id string, n *mail.NewImport) error {
	if !reFileID.MatchString(id) {
		return mail.ErrNotFound
	}
	m, err := s.Storages.FileMeta(ctx, id)
	if err != nil || m.Drive == "" || !(s.Drive.CanReadFile(ctx, sess.UserID, id, m.Drive, m.DriveOwner) ||
		(m.Drive == "user" && s.Drive.SharedFileTo(ctx, sess.UserID, id))) {
		return mail.ErrNotFound
	}
	if m.Size > mail.MaxImportSize {
		return i18n.E("err.mail_import_big")
	}
	head := []byte{}
	if m.Size > 0 {
		obj, err := s.Storages.Open(ctx, m.StorageID, m.Key)
		if err != nil {
			return err
		}
		head, _ = io.ReadAll(io.LimitReader(obj, 8))
		obj.Close()
	}
	if n.Format = mail.ImportFormat(m.Name, head); n.Format == "" {
		return i18n.E("err.mail_import_format")
	}
	n.FileID, n.Name = id, m.Name
	return nil
}

func (s *Server) mailImportDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DeleteImport(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, "/mail/import")
		return
	}
	http.Redirect(w, r, "/mail/import", http.StatusSeeOther)
}

// RunMailImports makes the imports waiting in the queue, one after another (go24 worker).
func (s *Server) RunMailImports(ctx context.Context) []error {
	var errs []error
	for ctx.Err() == nil {
		e, ok, err := s.Mail.ClaimImport(ctx)
		if err != nil {
			return append(errs, err)
		}
		if !ok {
			break
		}
		if err := s.runMailImport(ctx, e); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// runMailImport makes one import: the file is brought from the storage into a temporary file, its letters go to the
// mailbox.
func (s *Server) runMailImport(ctx context.Context, e mail.FileImport) error {
	lang := e.Lang
	if !s.I18n.Supported(lang) {
		lang = "ru-RU"
	}
	tr := func(key string, args ...any) string { return s.I18n.T(lang, key, args...) }
	e.Tr = tr
	n := e.ImportCount
	err := func() error {
		if e.FileID == "" {
			return i18n.E("err.mail_import_gone")
		}
		m, err := s.Storages.FileMeta(ctx, e.FileID)
		if err != nil {
			return i18n.E("err.mail_import_gone")
		}
		tmp, err := os.CreateTemp("", "go24-mail-import-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if m.Size > 0 {
			obj, err := s.Storages.Open(ctx, m.StorageID, m.Key)
			if err != nil {
				return err
			}
			_, err = io.Copy(tmp, obj)
			obj.Close()
			if err != nil {
				return err
			}
		}
		if _, err := tmp.Seek(0, 0); err != nil {
			return err
		}
		n, err = s.Mail.RunImport(ctx, e, tmp, func(c mail.ImportCount) { _ = s.Mail.ImportProgress(ctx, e.ID, c) })
		return err
	}()
	if err != nil {
		if ctx.Err() != nil { // go24 worker stops: the import goes on from its position when it is back
			_ = s.Mail.ImportProgress(context.WithoutCancel(ctx), e.ID, n)
			return nil
		}
		s.Log.Warn("mail import", "user_id", e.UserID, "import", e.ID, "err", err)
		_ = s.Mail.FinishImport(context.WithoutCancel(ctx), e.ID, n, s.I18n.Err(lang, err))
		s.notifyExport(ctx, e.UserID, "⚠ "+tr("mail.fimport.failed", e.Name, s.I18n.Err(lang, err)), "/mail/import")
		return nil
	}
	if err := s.Mail.FinishImport(ctx, e.ID, n, ""); err != nil {
		return err
	}
	s.Log.Info("mail import done", "user_id", e.UserID, "import", e.ID, "letters", n.Letters, "same", n.Same, "skipped", n.Skipped)
	s.notifyExport(ctx, e.UserID, tr("mail.fimport.done", e.Name, e.Email, n.Letters), "/mail/import")
	return nil
}

// ---------- REST ----------

func toAPIImport(e mail.FileImport) map[string]any {
	return map[string]any{"id": e.ID, "mailbox_id": e.MailboxID, "email": e.Email, "folder_id": e.FolderID, "file_id": e.FileID,
		"name": e.Name, "format": e.Format, "dupes": e.Dupes, "status": e.Status, "letters": e.Letters, "duplicates": e.Same,
		"skipped": e.Skipped, "position": e.Position, "folders": e.Folders, "error": e.Error, "created_at": e.CreatedAt,
		"finished_at": e.FinishedAt}
}

func (s *Server) apiMailImports(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Imports(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, e := range list {
		out = append(out, toAPIImport(e))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailImport(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	e, err := s.Mail.GetImport(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPIImport(e))
	return nil
}

// apiMailImportAdd imports a file of the drive (a file is put on the drive by the REST of «Диск» first).
func (s *Server) apiMailImportAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Mailbox string `json:"mailbox_id"`
		File    string `json:"file_id"`
		Folder  string `json:"folder_id"`
		Dupes   bool   `json:"dupes"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	n := mail.NewImport{MailboxID: in.Mailbox, FolderID: in.Folder, Dupes: in.Dupes, Lang: s.lang(r)}
	if err := s.importFromDrive(r.Context(), auth.Session{UserID: p.UserID}, in.File, &n); err != nil {
		return err
	}
	id, err := s.Mail.AddImport(r.Context(), p.UserID, n)
	if err != nil {
		return err
	}
	e, err := s.Mail.GetImport(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toAPIImport(e))
	return nil
}

func (s *Server) apiMailImportDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DeleteImport(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
