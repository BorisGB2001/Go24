package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/storages"
)

// ---------- «Почта»: «Экспорт» of letters to a file ----------

// As Outlook's «Импорт и экспорт → Экспорт в файл»: /mail/export asks what to export — a mailbox, its folders (with
// subfolders), a period — and in what format (.mbox or .eml, in a .zip); go24 worker makes the file (RunMailExports),
// puts it into «Выгрузка почты» of the user's drive and notifies the user; the page lists the exports and their state.

func (s *Server) mailExportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /mail/export", s.requireUser(s.mailExportPage))
	mux.HandleFunc("POST /mail/export", s.requireUser(s.mailExportAdd))
	mux.HandleFunc("POST /mail/export/{id}/delete", s.requireUser(s.mailExportDelete))
	mux.HandleFunc("GET /api/v1/mail/exports", s.api("mail:read", s.apiMailExports))
	mux.HandleFunc("POST /api/v1/mail/exports", s.api("mail:write", s.apiMailExportAdd))
	mux.HandleFunc("GET /api/v1/mail/exports/{id}", s.api("mail:read", s.apiMailExport))
	mux.HandleFunc("DELETE /api/v1/mail/exports/{id}", s.api("mail:write", s.apiMailExportDelete))
}

func (s *Server) mailExportPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
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
	exports, err := s.Mail.Exports(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	busy := false
	for _, e := range exports {
		busy = busy || e.Busy()
	}
	s.render(w, r, "mailexport", page{TitleKey: "mail.export.title", Nav: "mail", Data: map[string]any{
		"Boxes": boxes, "Box": box, "Folders": folders, "Folder": q.Get("folder"), "Formats": mail.ExportFormats,
		"Exports": exports, "Busy": busy, "Msg": q.Get("msg")}})
}

// exportForm reads what to export from the page's form.
func (s *Server) exportForm(r *http.Request) (mail.NewExport, error) {
	n := mail.NewExport{MailboxID: r.FormValue("box"), Folders: r.Form["folder"], Subfolders: r.FormValue("subfolders") != "",
		Format: r.FormValue("format"), Lang: s.lang(r)}
	var err error
	if n.Since, err = parseDate(r.FormValue("since")); err != nil {
		return n, err
	}
	n.Until, err = parseDate(r.FormValue("until"))
	return n, err
}

func (s *Server) mailExportAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.mailErr(w, r, i18n.E("err.bad_form"), "/mail/export")
		return
	}
	back := "/mail/export?box=" + url.QueryEscape(r.FormValue("box"))
	n, err := s.exportForm(r)
	if err == nil && r.FormValue("scope") == "all" {
		n.Folders = nil
	}
	if err == nil && r.FormValue("scope") != "all" && len(n.Folders) == 0 {
		err = i18n.E("err.mail_export_folders")
	}
	if err == nil {
		_, err = s.Mail.AddExport(r.Context(), sess.UserID, n)
	}
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("mail export queued", "user_id", sess.UserID, "mailbox", n.MailboxID, "format", n.Format, "folders", len(n.Folders))
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(s.tr(r, "mail.export.queued")), http.StatusSeeOther)
}

func (s *Server) mailExportDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DeleteExport(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, "/mail/export")
		return
	}
	http.Redirect(w, r, "/mail/export", http.StatusSeeOther)
}

// RunMailExports makes the exports waiting in the queue, one after another (go24 worker).
func (s *Server) RunMailExports(ctx context.Context) []error {
	var errs []error
	for ctx.Err() == nil {
		e, ok, err := s.Mail.ClaimExport(ctx)
		if err != nil {
			return append(errs, err)
		}
		if !ok {
			break
		}
		if err := s.runMailExport(ctx, e); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// runMailExport makes one export: the .pst or .zip is written to a temporary file, then put on the drive.
func (s *Server) runMailExport(ctx context.Context, e mail.Export) error {
	lang := e.Lang
	if !s.I18n.Supported(lang) {
		lang = "ru-RU"
	}
	tr := func(key string, args ...any) string { return s.I18n.T(lang, key, args...) }
	e.Tr = tr
	tmp, err := os.CreateTemp("", "go24-mail-export-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := s.Mail.WriteExport(ctx, e, tmp, func(n int) { _ = s.Mail.Progress(ctx, e.ID, n) })
	var size int64
	var fileID, to string
	var fi os.FileInfo
	if err == nil {
		fi, err = tmp.Stat() // a .pst is written at its places, not at the end
	}
	if err == nil {
		size = fi.Size()
		_, err = tmp.Seek(0, 0)
	}
	if err == nil {
		name, ctype := tr("mail.export.file", e.Email, time.Now().Format("2006-01-02"), e.Format), "application/zip"
		if e.Format == "pst" {
			name, ctype = tr("mail.export.file_pst", e.Email, time.Now().Format("2006-01-02")), "application/vnd.ms-outlook"
		}
		fileID, to, err = s.exportToDrive(ctx, e.UserID, tr("mail.export.folder"), name, ctype, tmp, size)
	}
	if err != nil {
		if ctx.Err() != nil { // go24 worker stops: the export is taken again when it is back
			return nil
		}
		s.Log.Warn("mail export", "user_id", e.UserID, "export", e.ID, "err", err)
		_ = s.Mail.FinishExport(context.WithoutCancel(ctx), e.ID, n, 0, "", "", s.I18n.Err(lang, err))
		s.notifyExport(ctx, e.UserID, "⚠ "+tr("mail.export.failed", e.Email, s.I18n.Err(lang, err)), "/mail/export")
		return nil
	}
	if err := s.Mail.FinishExport(ctx, e.ID, n, size, fileID, to, ""); err != nil {
		return err
	}
	s.Log.Info("mail export done", "user_id", e.UserID, "export", e.ID, "letters", n, "size", size)
	s.notifyExport(ctx, e.UserID, tr("mail.export.done", e.Email, n), to)
	return nil
}

// exportToDrive puts the file into the folder of the user's drive (made when missing) and tells where it is.
func (s *Server) exportToDrive(ctx context.Context, userID, folderName, name, ctype string, f *os.File, size int64) (fileID, to string, err error) {
	d, err := s.Drive.Open(ctx, userID, "my")
	if err != nil {
		return "", "", err
	}
	if err := s.Drive.Fits(ctx, d, size); err != nil {
		return "", "", err
	}
	folder, err := s.driveTopFolder(ctx, userID, d, folderName)
	if err != nil {
		return "", "", err
	}
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), path.Base(strings.ReplaceAll(name, "\\", "/")))
	if err := s.Storages.Put(ctx, userID, false, "", key, f, size, ctype); err != nil {
		return "", "", err
	}
	fileID, err = s.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: name, ContentType: ctype, Size: size,
		Drive: d.Kind, DriveOwner: d.OwnerID, FolderID: d.At(folder), UploadedBy: userID})
	return fileID, d.Path() + "?folder=" + url.QueryEscape(folder), err
}

// driveTopFolder is the folder of that name at the drive's top, made when missing (driveFolderNamed without a request).
func (s *Server) driveTopFolder(ctx context.Context, userID string, d drive.Drive, name string) (string, error) {
	folders, _, _, err := s.Drive.List(ctx, d, "")
	if err != nil {
		return "", err
	}
	for _, f := range folders {
		if strings.EqualFold(f.Name, name) {
			return f.ID, nil
		}
	}
	return s.Drive.CreateFolder(ctx, userID, d, "", name)
}

// notifyExport tells the user an export is over (the bell).
func (s *Server) notifyExport(ctx context.Context, userID, text, link string) {
	if _, err := s.DB.Exec(context.WithoutCancel(ctx), `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'mail', left($2, 300), $3)`,
		userID, text, link); err != nil {
		s.Log.Warn("mail export notify", "err", err)
	}
}

// ---------- REST ----------

func toAPIExport(e mail.Export) map[string]any {
	day := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return t.Format("2006-01-02")
	}
	return map[string]any{"id": e.ID, "mailbox_id": e.MailboxID, "email": e.Email, "folders": e.Folders, "subfolders": e.Subfolders,
		"since": day(e.Since), "until": day(e.Until), "format": e.Format, "status": e.Status, "letters": e.Letters, "size": e.Size,
		"file_id": e.FileID, "url": e.URL, "error": e.Error, "created_at": e.CreatedAt, "finished_at": e.FinishedAt}
}

func (s *Server) apiMailExports(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Exports(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, e := range list {
		out = append(out, toAPIExport(e))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailExport(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	e, err := s.Mail.GetExport(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPIExport(e))
	return nil
}

func (s *Server) apiMailExportAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Mailbox    string   `json:"mailbox_id"`
		Folders    []string `json:"folders"`
		Subfolders *bool    `json:"subfolders"`
		Since      string   `json:"since"`
		Until      string   `json:"until"`
		Format     string   `json:"format"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	n := mail.NewExport{MailboxID: in.Mailbox, Folders: in.Folders, Subfolders: in.Subfolders == nil || *in.Subfolders,
		Format: in.Format, Lang: s.lang(r)}
	if n.Format == "" {
		n.Format = "mbox"
	}
	var err error
	if n.Since, err = parseDate(in.Since); err != nil {
		return err
	}
	if n.Until, err = parseDate(in.Until); err != nil {
		return err
	}
	id, err := s.Mail.AddExport(r.Context(), p.UserID, n)
	if err != nil {
		return err
	}
	e, err := s.Mail.GetExport(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toAPIExport(e))
	return nil
}

func (s *Server) apiMailExportDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DeleteExport(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
