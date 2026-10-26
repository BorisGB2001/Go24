package web

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Почта»: the files of letters ----------

// As Outlook's attachments: a file opens in go24 without being downloaded (pictures, PDF in the browser's viewer,
// the text of office documents — see mail.Preview), one after another; «Скачать все» as a ZIP archive; «Сохранить на
// Диск» (one or all) into «Вложения почты» of «Мой диск» or another drive; a file of a drive goes with a letter as a
// copy, or as a link to it in go24 («Прикрепить с Диска»). Only the mailbox's owner reaches a letter's files.

var reFileID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// mailFileOf is a file of a letter of the user.
func (s *Server) mailFileOf(r *http.Request, sess auth.Session, id string) (storages.FileMeta, error) {
	if !reFileID.MatchString(id) {
		return storages.FileMeta{}, mail.ErrNotFound
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil || m.MailID == "" || !s.Mail.Owns(r.Context(), sess.UserID, m.MailID) {
		return storages.FileMeta{}, mail.ErrNotFound
	}
	return m, nil
}

// readFile is a kept file's bytes (bounded: a bigger one is an error).
func (s *Server) readFile(r *http.Request, m storages.FileMeta, limit int64) ([]byte, error) {
	if m.Size > limit {
		return nil, i18n.E("err.mail_preview_big")
	}
	if m.Size == 0 {
		return nil, nil
	}
	obj, err := s.Storages.Open(r.Context(), m.StorageID, m.Key)
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(io.LimitReader(obj, limit+1))
}

// mailFilePreview is how a file of a letter is shown in the viewer: kind (mail.PreviewImage, PreviewPDF, PreviewHTML,
// "" — download it) and the HTML of a document.
func (s *Server) mailFilePreview(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.mailFileOf(r, sess, r.PathValue("id"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := map[string]any{"id": r.PathValue("id"), "name": m.Name, "size_text": s.sizeText(r, m.Size), "kind": mail.PreviewKind(m.Name, m.ContentType)}
	if out["kind"] == mail.PreviewHTML {
		data, err := s.readFile(r, m, 20<<20)
		if err == nil {
			if h, ok := mail.Preview(m.Name, m.ContentType, data); ok {
				out["html"] = h
			} else {
				out["kind"] = mail.PreviewNone
			}
		} else {
			out["kind"] = mail.PreviewNone
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// mailFileRaw is a picture or a PDF of a letter for the viewer: shown in the browser, never run (no scripts).
func (s *Server) mailFileRaw(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.mailFileOf(r, sess, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	ct := "application/pdf"
	switch mail.PreviewKind(m.Name, m.ContentType) {
	case mail.PreviewImage:
		ct = m.ContentType
		if !strings.HasPrefix(ct, "image/") {
			ct = "application/octet-stream" // a picture by its name only: the browser guesses no further (nosniff)
		}
	case mail.PreviewPDF:
	default:
		http.Redirect(w, r, "/files/"+r.PathValue("id"), http.StatusSeeOther)
		return
	}
	obj, err := s.Storages.Open(r.Context(), m.StorageID, m.Key)
	if err != nil {
		s.fail(w, "open file", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; object-src 'self'")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename*=UTF-8''%s", url.PathEscape(m.Name)))
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("mail file", "err", err)
	}
}

// zipName is a file's name in an archive, unique: «a.pdf», «a (2).pdf».
func zipName(name string, seen map[string]int) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = "file"
	}
	key := strings.ToLower(name)
	seen[key]++
	if n := seen[key]; n > 1 {
		ext := path.Ext(name)
		name = strings.TrimSuffix(name, ext) + " (" + strconv.Itoa(n) + ")" + ext
		seen[strings.ToLower(name)]++
	}
	return name
}

// mailFilesZip is «Скачать все»: the files of a letter (letters: of a conversation) in a ZIP archive named after its
// subject.
func (s *Server) mailFilesZip(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ids := letterIDs(append([]string{r.PathValue("letter")}, r.URL.Query()["letter"]...))
	m, err := s.Mail.Message(r.Context(), sess.UserID, ids[0])
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	var files []mail.File
	for _, id := range ids {
		list, err := s.Mail.Files(r.Context(), sess.UserID, id)
		if err != nil {
			s.taskErr(w, r, err, "/mail")
			return
		}
		files = append(files, list...)
	}
	if len(files) == 0 {
		s.taskErr(w, r, mail.ErrNotFound, mailURL(m.FolderID, m.ID))
		return
	}
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '_'
		}
		return r
	}, strings.TrimSpace(m.Subject))
	if name == "" {
		name = "mail"
	}
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(name+".zip")))
	z := zip.NewWriter(w)
	seen := map[string]int{}
	for _, f := range files {
		meta, err := s.Storages.FileMeta(r.Context(), f.ID)
		if err != nil {
			s.Log.Error("mail zip", "err", err)
			return
		}
		zw, err := z.CreateHeader(&zip.FileHeader{Name: zipName(f.Name, seen), Method: zip.Deflate, Modified: m.SentAt})
		if err != nil {
			return
		}
		if meta.Size == 0 {
			continue
		}
		obj, err := s.Storages.Open(r.Context(), meta.StorageID, meta.Key)
		if err != nil {
			s.Log.Error("mail zip", "err", err)
			return
		}
		_, err = io.Copy(zw, obj)
		obj.Close()
		if err != nil {
			return
		}
	}
	if err := z.Close(); err != nil {
		s.Log.Error("mail zip", "err", err)
	}
}

// mailDriveTarget is the drive (my, company, a project's key) and its folder files go to; no folder — «Вложения
// почты» at its top, made when missing (as Outlook saves to OneDrive's «Вложения электронной почты»).
func (s *Server) mailDriveTarget(r *http.Request, sess auth.Session, which, folder string) (drive.Drive, string, error) {
	if which == "" {
		which = "my"
	}
	d, err := s.Drive.Open(r.Context(), sess.UserID, which)
	if err != nil {
		return d, "", err
	}
	if folder != "" {
		return d, folder, s.Drive.CheckFolder(r.Context(), d, folder)
	}
	if err := s.Drive.CheckFolder(r.Context(), d, ""); err != nil {
		return d, "", err
	}
	folders, _, _, err := s.Drive.List(r.Context(), d, "")
	if err != nil {
		return d, "", err
	}
	name := s.tr(r, "mail.drive_folder")
	for _, f := range folders {
		if strings.EqualFold(f.Name, name) {
			return d, f.ID, nil
		}
	}
	id, err := s.Drive.CreateFolder(r.Context(), sess.UserID, d, "", name)
	return d, id, err
}

// mailFilesToDrive is «Сохранить на Диск»: files of letters (file=…) or all the files of letters (letter=…) are
// copied to a drive.
func (s *Server) mailFilesToDrive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.mailErr(w, r, i18n.E("err.bad_form"), "/mail")
		return
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	var metas []storages.FileMeta
	for _, id := range r.Form["file"] {
		m, err := s.mailFileOf(r, sess, id)
		if err != nil {
			s.mailErr(w, r, err, back)
			return
		}
		metas = append(metas, m)
	}
	for _, letter := range letterIDs(r.Form["letter"]) {
		list, err := s.Mail.Files(r.Context(), sess.UserID, letter)
		if err != nil {
			s.mailErr(w, r, err, back)
			return
		}
		for _, f := range list {
			m, err := s.Storages.FileMeta(r.Context(), f.ID)
			if err != nil {
				s.mailErr(w, r, err, back)
				return
			}
			metas = append(metas, m)
		}
	}
	if len(metas) == 0 {
		s.mailErr(w, r, i18n.E("err.file_required"), back)
		return
	}
	d, folder, err := s.mailDriveTarget(r, sess, r.FormValue("drive"), r.FormValue("folder"))
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	var total int64
	for _, m := range metas {
		total += m.Size
	}
	if err := s.Drive.Fits(r.Context(), d, total); err != nil { // the drive's limit
		s.mailErr(w, r, err, back)
		return
	}
	for _, m := range metas {
		if err := s.copyFile(r, sess, m, storages.NewFile{Drive: d.Kind, DriveOwner: d.OwnerID, FolderID: d.At(folder)}); err != nil {
			s.mailErr(w, r, err, back)
			return
		}
	}
	s.Log.Info("mail files saved to drive", "user_id", sess.UserID, "files", len(metas), "drive", d.Path())
	to := d.Path() + "?folder=" + url.QueryEscape(folder)
	text := s.tr(r, "mail.drive_saved", len(metas))
	if fromForm(r) || wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"saved": len(metas), "url": to, "text": text})
		return
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(text), http.StatusSeeOther)
}

// copyFile copies a kept file's bytes into a new file (in the built-in storage) placed as to says.
func (s *Server) copyFile(r *http.Request, sess auth.Session, m storages.FileMeta, to storages.NewFile) error {
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), path.Base(strings.ReplaceAll(m.Name, "\\", "/")))
	var src io.Reader = bytes.NewReader(nil)
	if m.Size > 0 {
		obj, err := s.Storages.Open(r.Context(), m.StorageID, m.Key)
		if err != nil {
			return err
		}
		defer obj.Close()
		src = obj
	}
	if err := s.Storages.Put(r.Context(), sess.UserID, false, "", key, src, m.Size, m.ContentType); err != nil {
		return err
	}
	to.Key, to.Name, to.ContentType, to.Size, to.UploadedBy = key, m.Name, m.ContentType, m.Size, sess.UserID
	_, err := s.Storages.AddFile(r.Context(), to)
	return err
}

// mailDrive is the drive picker of the letter's editor and of «Сохранить на Диск»: the drives the user may use, a
// folder of one (which=my|company|KEY, folder=…) with its folders and files.
func (s *Server) mailDrive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	which := q.Get("which")
	if which == "" {
		which = "my"
	}
	drives := []map[string]any{{"which": "my", "name": s.tr(r, "disk.my")}, {"which": "company", "name": s.tr(r, "disk.company")}}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	for _, p := range projects {
		if !p.Archived {
			drives = append(drives, map[string]any{"which": p.Key, "name": p.Name})
		}
	}
	shared, err := s.Drive.SharedWithMe(r.Context(), sess.UserID) // «Общие со мной»
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	for _, f := range shared {
		drives = append(drives, map[string]any{"which": "sh:" + f.ID, "name": f.Name + " (" + f.Owner + ")"})
	}
	d, err := s.Drive.Open(r.Context(), sess.UserID, which)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if d.Employee { // an administrator in an employee's drive: it is among those to pick
		drives = append(drives, map[string]any{"which": which, "name": s.tr(r, "disk.employee", d.Owner)})
	}
	folders, files, trail, err := s.Drive.List(r.Context(), d, q.Get("folder"))
	if err == nil { // «write»: in this folder, by the rights of the folders
		d, err = s.Drive.Within(r.Context(), d, d.At(q.Get("folder")))
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	fl := []map[string]any{}
	for _, f := range folders {
		fl = append(fl, map[string]any{"id": f.ID, "name": f.Name})
	}
	tr := []map[string]any{}
	for _, f := range trail {
		tr = append(tr, map[string]any{"id": f.ID, "name": f.Name})
	}
	fs := []map[string]any{}
	for _, f := range files {
		fs = append(fs, map[string]any{"id": f.ID, "name": f.Name, "size": f.Size, "size_text": s.sizeText(r, f.Size)})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"drives": drives, "which": which, "write": d.CanWrite, "path": tr, "folders": fl, "files": fs})
}

// driveFileReadable: the user sees the drive of the file, or the folder of it shared with them.
func (s *Server) driveFileReadable(r *http.Request, sess auth.Session, id string, m storages.FileMeta) bool {
	return s.Drive.CanReadFile(r.Context(), sess.UserID, id, m.Drive, m.DriveOwner) || (m.Drive == "user" && s.Drive.SharedFileTo(r.Context(), sess.UserID, id))
}

// mailDraftDriveFile puts a copy of a drive's file into a draft («Прикрепить с Диска»: копией).
func (s *Server) mailDraftDriveFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.FormValue("file")
	if !reFileID.MatchString(id) {
		s.jsonErr(w, r, mail.ErrNotFound)
		return
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil || m.Drive == "" || !s.driveFileReadable(r, sess, id, m) {
		s.jsonErr(w, r, mail.ErrNotFound)
		return
	}
	if m.Size > mail.MaxSendSize {
		s.jsonErr(w, r, i18n.E("err.mail_files", mail.MaxSendFiles))
		return
	}
	data, err := s.readFile(r, m, mail.MaxSendSize)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	file, err := s.Mail.AttachDraft(r.Context(), sess.UserID, r.PathValue("id"), mail.Attachment{Name: m.Name, ContentType: m.ContentType, Data: data}, false)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": file.ID, "name": file.Name, "size": file.Size, "size_text": s.sizeText(r, file.Size),
		"url": "/files/" + file.ID + "?inline=1", "image": file.Image()})
}

// mailDriveLink is a drive's file as a link in a letter («Прикрепить с Диска»: ссылкой): its name and its address in
// go24 (those who may see the drive open it).
func (s *Server) mailDriveLink(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.URL.Query().Get("file")
	if !reFileID.MatchString(id) {
		s.jsonErr(w, r, mail.ErrNotFound)
		return
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil || m.Drive == "" || !s.driveFileReadable(r, sess, id, m) {
		s.jsonErr(w, r, mail.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": m.Name, "url": s.absURL(r, "/files/"+id), "size_text": s.sizeText(r, m.Size),
		"guest": tasks.IsGuestCtx(r.Context())})
}

// absURL is go24's address of a path (its configured address, else as the browser reached it).
func (s *Server) absURL(r *http.Request, p string) string {
	if s.BaseURL != "" {
		return strings.TrimSuffix(s.BaseURL, "/") + p
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + p
}
