package web

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/office"
	"go24/internal/tasks"
)

// ---------- «Публичная ссылка» of «Диск»: /s/{token} for anyone, no sign-in ----------

// diskLink makes or changes the public link of a file (file=ID) or a folder (folder_id=ID): expires (a date, the
// link works through that day), password (clear_password=1 — none), edit=1 — a document edited by those who open
// it; off=1 switches it off.
func (s *Server) diskLink(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	back := cardBack(r, diskBack(d, r.FormValue("folder")))
	kind, id := "file", r.FormValue("file")
	if id == "" {
		kind, id = "folder", r.FormValue("folder_id")
	}
	if r.FormValue("off") == "1" {
		err = s.Drive.DeleteLink(r.Context(), sess.UserID, d, kind, id)
	} else {
		var expires *time.Time
		if v := r.FormValue("expires"); v != "" {
			day, perr := time.ParseInLocation("2006-01-02", v, time.Local)
			if perr != nil {
				s.taskErr(w, r, i18n.E("err.link_expired_date"), back)
				return
			}
			end := day.AddDate(0, 0, 1).Add(-time.Second)
			expires = &end
		}
		_, err = s.Drive.SetLink(r.Context(), sess.UserID, d, kind, id, r.FormValue("password"), r.FormValue("clear_password") == "1", expires,
			r.FormValue("edit") == "1", driveWhich(d))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func shareCookie(token string) string { return "go24s_" + token }

// shareOpen finds the link of the request; with a password not typed yet, the page asks for it (ok false).
func (s *Server) shareOpen(w http.ResponseWriter, r *http.Request) (drive.Shared, bool) {
	sh, err := s.Drive.OpenLink(r.Context(), r.PathValue("token"))
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return sh, false
	}
	if sh.Password {
		if c, err := r.Cookie(shareCookie(sh.Token)); err != nil || c.Value != sh.PasswordStamp() {
			if r.URL.Path != "/s/"+sh.Token { // a file or the archive: not before the password
				w.WriteHeader(http.StatusUnauthorized)
			}
			s.render(w, r, "share", page{TitleKey: "share.title", Data: map[string]any{"AskPassword": true, "Token": sh.Token,
				"Wrong": r.Method == http.MethodPost}})
			return sh, false
		}
	}
	return sh, true
}

// sharePage is what the link opens: a file to download (a picture shown), or a folder to walk through (?folder=).
func (s *Server) sharePage(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.shareOpen(w, r)
	if !ok {
		return
	}
	data := map[string]any{"Link": sh, "Token": sh.Token, "Name": sh.Name, "Size": sh.Size}
	if sh.FileID != "" {
		data["Kind"] = fileKind(sh.Name, "")
		data["Board"] = drive.IsBoard(sh.Name) // shown read only, in the editor of «Доски»
		data["Doc"] = s.Office != nil && office.Kind(sh.Name) != ""
		data["Edit"] = data["Doc"] == true && s.linkEditor(r.Context(), sh) != ""
	} else {
		folders, files, trail, err := s.Drive.SharedList(r.Context(), sh, r.URL.Query().Get("folder"))
		if err != nil {
			s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
			return
		}
		data["Folders"], data["Files"], data["Trail"] = folders, files, trail
		data["Folder"] = trail[len(trail)-1].ID
		data["Office"] = s.Office != nil
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.render(w, r, "share", page{Title: sh.Name, Data: data})
}

// shareDoc opens a document of the link in ONLYOFFICE: for viewing, or for editing when the link lets one edit the
// document and its maker still may change it (see linkEditor). Whoever opens it is a guest the editor asks the name of.
func (s *Server) shareDoc(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.shareOpen(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	m, err := s.Storages.FileMeta(r.Context(), id)
	if s.Office == nil || s.Drive.SharedFile(r.Context(), sh, id) != nil || err != nil || office.Kind(m.Name) == "" {
		http.NotFound(w, r)
		return
	}
	back := "/s/" + sh.Token
	if sh.FolderID != "" {
		if f, err := s.Drive.FileOn(r.Context(), sh.Drive, id); err == nil && f.FolderID != sh.FolderID {
			back += "?folder=" + url.QueryEscape(f.FolderID)
		}
	}
	which := s.linkEditor(r.Context(), sh)
	edit := which != "" && sh.FileID == id && office.Editable(m.Name)
	callback := "/onlyoffice/callback/" + id + "?" + url.Values{"u": {sh.By}, "w": {which}, "s": {s.Office.Stamp(id, sh.By, which)}}.Encode()
	raw, err := s.officeConfig(r, auth.Session{UserID: "guest-" + randomHex(8)}, m.Name, office.Key(id, m.Key),
		"/onlyoffice/file/"+id+"/"+s.Office.Stamp(id, m.Key), callback, back, edit)
	if err != nil {
		s.fail(w, "office config", err)
		return
	}
	s.letOfficeGuest(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.render(w, r, "office", page{Title: m.Name, Data: map[string]any{"Config": raw, "Name": m.Name, "Back": back,
		"BackKey": "share.back", "Edit": edit, "Download": "/s/" + sh.Token + "/f/" + id}})
}

// linkEditor is how the link's maker opens the drive when the link lets one edit its document and the maker (still
// working) may change the file now — not locked by someone, not moved where the maker no longer writes; "" — not so.
func (s *Server) linkEditor(ctx context.Context, sh drive.Shared) string {
	if !sh.Edit || sh.FileID == "" || sh.As == "" {
		return ""
	}
	var guest, active bool
	if err := s.DB.QueryRow(ctx, `SELECT active, guest FROM users WHERE id::text = $1`, sh.By).Scan(&active, &guest); err != nil || !active {
		return ""
	}
	ctx = tasks.WithViewer(ctx, sh.By, guest)
	d, err := s.Drive.Open(ctx, sh.By, sh.As)
	if err != nil {
		return ""
	}
	if may, err := s.Drive.MayChange(ctx, sh.By, d, sh.FileID); err != nil || !may {
		return ""
	}
	return sh.As
}

// sharePassword takes the password of a link and remembers it in the browser for a day.
func (s *Server) sharePassword(w http.ResponseWriter, r *http.Request) {
	sh, err := s.Drive.OpenLink(r.Context(), r.PathValue("token"))
	if err == nil && sh.CheckPassword(r.FormValue("password")) {
		http.SetCookie(w, &http.Cookie{Name: shareCookie(sh.Token), Value: sh.PasswordStamp(), Path: "/s/" + sh.Token, MaxAge: 86400,
			HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/s/"+sh.Token, http.StatusSeeOther)
		return
	}
	time.Sleep(time.Second) // guessing is slow
	r.Method = http.MethodPost
	s.shareOpen(w, r)
}

// shareFile gives a file of the link: the linked file or one within the shared folder (?inline=1 — a picture shown).
func (s *Server) shareFile(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.shareOpen(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := s.Drive.SharedFile(r.Context(), sh, id); err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var obj io.ReadCloser = io.NopCloser(strings.NewReader(""))
	if m.Size > 0 {
		if obj, err = s.Storages.Open(r.Context(), m.StorageID, m.Key); err != nil {
			s.fail(w, "open file", err)
			return
		}
	}
	defer obj.Close()
	disposition := "attachment"
	ct := m.ContentType
	if r.URL.Query().Get("inline") == "1" && fileKind(m.Name, m.ContentType) == mail.PreviewImage && strings.HasPrefix(ct, "image/") {
		disposition = "inline"
	} else if !(r.URL.Query().Has("scene") && drive.IsBoard(m.Name)) { // a board shown on the page is not a download

		s.Drive.Downloaded(r.Context(), sh.Token)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename*=UTF-8''%s", disposition, url.PathEscape(m.Name)))
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("shared file", "err", err)
	}
}

// shareZip is a shared folder (or a folder within it, ?folder=) as one ZIP archive.
func (s *Server) shareZip(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.shareOpen(w, r)
	if !ok || sh.FolderID == "" {
		if ok {
			http.NotFound(w, r)
		}
		return
	}
	folder := r.URL.Query().Get("folder")
	if _, _, _, err := s.Drive.SharedList(r.Context(), sh, folder); err != nil {
		http.NotFound(w, r)
		return
	}
	if folder == "" {
		folder = sh.FolderID
	}
	entries, err := s.Drive.Archive(r.Context(), sh.Drive, drive.Selection{Folders: []string{folder}})
	if err != nil {
		s.fail(w, "shared zip", err)
		return
	}
	s.Drive.Downloaded(r.Context(), sh.Token)
	s.writeZip(r.Context(), w, sh.Name, entries)
}

// writeZip streams files of a drive as one ZIP archive.
func (s *Server) writeZip(ctx context.Context, w http.ResponseWriter, name string, entries []drive.Entry) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(name+".zip")))
	zw := zip.NewWriter(w)
	used := map[string]int{}
	for _, e := range entries {
		p := e.Path
		if n := used[strings.ToLower(p)]; n > 0 { // two files of one name in a folder: the archive keeps both
			ext := path.Ext(p)
			p = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(p, ext), n+1, ext)
		}
		used[strings.ToLower(e.Path)]++
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: p, Method: zip.Deflate, Modified: e.ModTime})
		if err != nil {
			s.Log.Error("zip", "err", err)
			return
		}
		if e.Size == 0 {
			continue
		}
		obj, err := s.Storages.Open(ctx, e.StorageID, e.Key)
		if err != nil {
			s.Log.Error("zip open", "key", e.Key, "err", err)
			return
		}
		_, err = io.Copy(fw, obj)
		obj.Close()
		if err != nil {
			s.Log.Error("zip copy", "key", e.Key, "err", err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.Log.Error("zip", "err", err)
	}
}
