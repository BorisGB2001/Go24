package web

import (
	"context"
	"encoding/base64"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/webdav"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/office"
	"go24/internal/tasks"
)

// ---------- «Редактировать на компьютере»: a document of the drive in Microsoft Office or LibreOffice ----------
//
// The file's menu opens it in the program on the computer by its own link scheme (ms-word:ofe|u|…). The program
// takes the file over WebDAV at /dav-edit/{token}/{name}: a signed link to the one file for the one employee, no
// password asked; it works for davEditTTL, and every request checks again that the employee may see (and, to save,
// change) the file. Saving in the program («Сохранить», Ctrl+S) makes the file's new version; while the program has
// the file locked it shows as being edited in the drive's list.

const davEditTTL = 24 * time.Hour

// davEditApps are the schemes of the programs by kind of document, Microsoft Office's first.
func davEditApps(name string) [][2]string {
	ms := map[string]string{"word": "ms-word", "cell": "ms-excel", "slide": "ms-powerpoint"}[office.Kind(name)]
	if ms == "" || office.Ext(name) == "txt" {
		return nil
	}
	return [][2]string{{"msoffice", ms + ":ofe|u|"}, {"libreoffice", "vnd.libreoffice.command:ofe|u|"}}
}

// davEditToken signs the link of the employee to the file of the drive (as Drive.Open finds it) till exp.
func (s *Server) davEditToken(userID, which, fileID string, exp time.Time) string {
	payload := strings.Join([]string{userID, which, fileID, strconv.FormatInt(exp.Unix(), 36)}, "~")
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + s.Sessions.Stamp("dav-edit", payload)
}

// davEditOpen reads a token: the employee, the drive and the file; false — it is not go24's or has expired.
func (s *Server) davEditOpen(token string) (userID, which, fileID string, ok bool) {
	data, stamp, _ := strings.Cut(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(data)
	if err != nil || !s.Sessions.Stamped(stamp, "dav-edit", string(raw)) {
		return "", "", "", false
	}
	parts := strings.Split(string(raw), "~")
	if len(parts) != 4 {
		return "", "", "", false
	}
	exp, err := strconv.ParseInt(parts[3], 36, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// diskLocal is the page «Редактировать на компьютере» of a document: the buttons that open it in the programs and
// its address for any other program that opens files over WebDAV.
func (s *Server) diskLocal(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	back := diskBack(d, r.URL.Query().Get("folder"))
	id := r.PathValue("file")
	f, err := s.Drive.FileOn(r.Context(), d, id)
	if err == nil && (tasks.IsGuestCtx(r.Context()) || davEditApps(f.Name) == nil) {
		err = drive.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	may, err := s.Drive.MayChange(r.Context(), sess.UserID, d, id)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	link := s.CallURL // the programs ask for https
	if link == "" {
		link = s.absURL(r, "")
	}
	link += "/dav-edit/" + s.davEditToken(sess.UserID, driveWhichOf(r, d), id, time.Now().Add(davEditTTL)) + "/" + url.PathEscape(davName(f.Name))
	var apps []map[string]any
	for _, a := range davEditApps(f.Name) { // the programs' own schemes, given as they are: the programs want the «|»
		apps = append(apps, map[string]any{"App": a[0], "Href": template.HTMLAttr(`href="` + template.HTMLEscapeString(a[1]+link) + `"`)})
	}
	s.render(w, r, "disklocal", page{Title: f.Name, Nav: "disk", Data: map[string]any{"Name": f.Name, "Back": back,
		"Edit": may, "Apps": apps, "Link": link, "TTL": int(davEditTTL / time.Hour)}})
}

// driveWhichOf is how Drive.Open finds the drive of the request again (an employee's drive opened by an
// administrator too).
func driveWhichOf(r *http.Request, d drive.Drive) string {
	if uid := r.PathValue("uid"); uid != "" {
		return "u:" + uid
	}
	return driveWhich(d)
}

// davEdit serves the one file of a token over WebDAV.
func (s *Server) davEdit(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/dav-edit/")
	token, _, _ := strings.Cut(rest, "/")
	userID, which, fileID, ok := s.davEditOpen(token)
	var active, guest bool
	if ok {
		ok = s.DB.QueryRow(r.Context(), `SELECT active, guest FROM users WHERE id::text = $1`, userID).Scan(&active, &guest) == nil && active && !guest
	}
	if !ok {
		http.Error(w, s.tr(r, "err.dav_edit_link"), http.StatusForbidden)
		return
	}
	ctx := tasks.WithViewer(r.Context(), userID, false)
	r = r.WithContext(ctx)
	d, err := s.Drive.Open(ctx, userID, which)
	var f drive.File
	if err == nil {
		f, err = s.Drive.FileOn(ctx, d, fileID)
	}
	if err != nil {
		http.Error(w, s.tr(r, "err.dav_edit_link"), http.StatusNotFound)
		return
	}
	switch r.Method { // what changes: only the file, by one who may change it (a program refused a LOCK opens it read only)
	case "GET", "HEAD", "OPTIONS", "PROPFIND", "UNLOCK":
	case "DELETE", "MKCOL", "MOVE", "COPY":
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	default:
		_, name, _ := strings.Cut(rest, "/")
		may, err := s.Drive.MayChange(ctx, userID, d, fileID)
		if err != nil || !may || strings.Trim(path.Clean("/"+name), "/") != davName(f.Name) {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
	}
	ls, _ := davLocks.LoadOrStore("edit:"+userID, webdav.NewMemLS())
	h := &webdav.Handler{Prefix: "/dav-edit/" + token, FileSystem: &davOneFS{s: s, r: r, user: userID, d: d, f: f},
		LockSystem: ls.(webdav.LockSystem), Logger: func(r *http.Request, err error) {
			if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
				s.Log.Warn("dav-edit", "method", r.Method, "file", fileID, "err", err)
			}
		}}
	rec := &statusWriter{ResponseWriter: w}
	h.ServeHTTP(rec, r)
	switch { // the program holds the file: «Редактируется» in the list till it lets it go
	case r.Method == "LOCK" && rec.status < 300:
		err = s.Drive.Editing(ctx, fileID, []string{userID})
	case r.Method == "UNLOCK" && rec.status < 300:
		err = s.Drive.Editing(ctx, fileID, nil)
	}
	if err != nil {
		s.Log.Warn("dav-edit editing", "file", fileID, "err", err)
	}
}

// statusWriter remembers the status of the answer.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

// davOneFS is a folder that holds the one file.
type davOneFS struct {
	s    *Server
	r    *http.Request
	user string
	d    drive.Drive
	f    drive.File
}

// isFile: the path names the file (by its name now); false — the folder; else it names nothing.
func (fs *davOneFS) isFile(name string) (bool, error) {
	switch strings.Trim(path.Clean("/"+name), "/") {
	case "":
		return false, nil
	case davName(fs.f.Name):
		return true, nil
	}
	return false, os.ErrNotExist
}

func (fs *davOneFS) Mkdir(context.Context, string, os.FileMode) error { return os.ErrPermission }
func (fs *davOneFS) RemoveAll(context.Context, string) error          { return os.ErrPermission }
func (fs *davOneFS) Rename(context.Context, string, string) error     { return os.ErrPermission }

func (fs *davOneFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	file, err := fs.isFile(name)
	if err != nil {
		return nil, err
	}
	if !file {
		return davInfo{name: "/", dir: true}, nil
	}
	return fs.info(), nil
}

func (fs *davOneFS) info() davInfo {
	return davInfo{name: davName(fs.f.Name), size: fs.f.Size, mod: fs.f.CreatedAt, ct: fs.f.ContentType}
}

func (fs *davOneFS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	file, err := fs.isFile(name)
	if err != nil {
		return nil, err
	}
	if !file {
		return &davOneDir{fs: fs}, nil
	}
	node := davNode{name: fs.f.Name, top: driveWhich(fs.d), drive: fs.d, folder: fs.f.FolderID, file: &fs.f}
	read := &davFile{fs: &davFS{s: fs.s, r: fs.r, user: fs.user}, node: node}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC) == 0 {
		return read, nil
	}
	if may, err := fs.s.Drive.MayChange(ctx, fs.user, fs.d, fs.f.ID); err != nil || !may {
		return nil, os.ErrPermission
	}
	tmp, err := os.CreateTemp("", "go24-dav-*")
	if err != nil {
		return nil, err
	}
	return &davOneWrite{davFile: read, one: fs, w: tmp}, nil
}

// davOneDir is the folder of the one file.
type davOneDir struct {
	fs   *davOneFS
	done bool
}

func (d *davOneDir) Close() error                       { return nil }
func (d *davOneDir) Read([]byte) (int, error)           { return 0, os.ErrInvalid }
func (d *davOneDir) Seek(int64, int) (int64, error)     { return 0, os.ErrInvalid }
func (d *davOneDir) Write([]byte) (int, error)          { return 0, os.ErrPermission }
func (d *davOneDir) Stat() (fs.FileInfo, error)         { return davInfo{name: "/", dir: true}, nil }
func (d *davOneDir) Readdir(count int) ([]fs.FileInfo, error) {
	if d.done && count > 0 {
		return nil, io.EOF
	}
	d.done = true
	return []fs.FileInfo{d.fs.info()}, nil
}

// davOneWrite is the file written by the program: into a temporary file, and as the file's new version when closed.
type davOneWrite struct {
	*davFile
	one *davOneFS
	w   *os.File
}

func (f *davOneWrite) Read([]byte) (int, error) { return 0, os.ErrInvalid }
func (f *davOneWrite) Write(p []byte) (int, error) { return f.w.Write(p) }
func (f *davOneWrite) Seek(offset int64, whence int) (int64, error) {
	return f.w.Seek(offset, whence)
}

func (f *davOneWrite) Close() error {
	defer os.Remove(f.w.Name())
	defer f.w.Close()
	st, err := f.w.Stat()
	if err != nil {
		return err
	}
	if _, err := f.w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return davErr(f.one.s.diskVersion(f.one.r.Context(), f.one.user, f.one.d, f.one.f.ID, f.w, st.Size()))
}

// diskVersion makes the bytes the file's new version by the user: in the file's storage, or in the built-in one
// when that is not the user's to write into; the drive's limit counts it.
func (s *Server) diskVersion(ctx context.Context, userID string, d drive.Drive, id string, data io.ReadSeeker, size int64) error {
	m, err := s.Storages.FileMeta(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Drive.Fits(ctx, d, size); err != nil {
		return err
	}
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), m.Name)
	storageID := ""
	if m.StorageID != nil {
		storageID = *m.StorageID
	}
	if err := s.Storages.Put(ctx, userID, false, storageID, key, data, size, m.ContentType); err != nil {
		if storageID == "" {
			return err
		}
		if _, err := data.Seek(0, io.SeekStart); err != nil {
			return err
		}
		storageID = ""
		if err := s.Storages.Put(ctx, userID, false, "", key, data, size, m.ContentType); err != nil {
			return err
		}
	}
	return s.Drive.AddVersion(ctx, userID, d, id, drive.NewVersion{Key: key, ContentType: m.ContentType, StorageID: storageID, Size: size})
}
