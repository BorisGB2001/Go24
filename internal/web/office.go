package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/office"
	"go24/internal/tasks"
)

// ---------- «Документы онлайн»: files of the drive in ONLYOFFICE ----------

// maxOfficeFile is the largest edited file go24 takes back from the document server.
const maxOfficeFile = 200 << 20

// driveWhich is how Drive.Open finds the drive again for the user who opened it.
func driveWhich(d drive.Drive) string {
	switch {
	case d.Root != "":
		return "sh:" + d.Root
	case d.File != "":
		return "sf:" + d.File
	case d.Kind == "company":
		return "company"
	case d.Kind == "project":
		return d.ProjectKey
	}
	return "my"
}

// officeEdit is the editor of a file of the drive: editing for those who may change the file, viewing for the rest.
func (s *Server) officeEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Office == nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	id := r.PathValue("file")
	back := docsBack(r, diskBack(d, r.URL.Query().Get("folder")))
	may, err := s.Drive.MayChange(r.Context(), sess.UserID, d, id)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err == nil && office.Kind(m.Name) == "" {
		err = drive.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	edit := may && office.Editable(m.Name)
	which := driveWhich(d)
	raw, err := s.officeConfig(r, sess, m.Name, office.Key(id, m.Key), "/onlyoffice/file/"+id+"/"+s.Office.Stamp(id, m.Key),
		"/onlyoffice/callback/"+id+"?"+url.Values{"u": {sess.UserID}, "w": {which}, "s": {s.Office.Stamp(id, sess.UserID, which)}}.Encode(),
		back, edit)
	if err != nil {
		s.fail(w, "office config", err)
		return
	}
	s.render(w, r, "office", page{Title: m.Name, Nav: "disk", Data: map[string]any{"Config": raw, "Name": m.Name,
		"Back": back, "Edit": edit, "Download": "/files/" + id}})
}

// officeConfig is the signed configuration of the editor of a document: its key (a new one for each state of the
// file), go24's links for the document server to take the file and to say of it (signed paths of go24), where
// «Назад» leads, and whether the user edits it.
func (s *Server) officeConfig(r *http.Request, sess auth.Session, title, key, file, callback, back string, edit bool) (string, error) {
	lang, _, _ := strings.Cut(s.lang(r), "-")
	mode := "view"
	if edit {
		mode = "edit"
	}
	name := sess.Name // co-editors see it; a session made without one takes it from the profile
	if name == "" {
		_ = s.DB.QueryRow(r.Context(), `SELECT name FROM users WHERE id::text = $1`, sess.UserID).Scan(&name)
	}
	cfg := map[string]any{
		"documentType": office.Kind(title), "width": "100%", "height": "100%",
		"document": map[string]any{
			"fileType": office.Ext(title), "key": key, "title": title, "url": s.Office.Self + file,
			"permissions": map[string]any{"edit": edit, "comment": edit, "review": edit, "download": true, "print": true},
		},
		"editorConfig": map[string]any{
			"mode": mode, "lang": lang, "callbackUrl": s.Office.Self + callback,
			"user":          map[string]any{"id": sess.UserID, "name": name},
			"customization": map[string]any{"goback": map[string]any{"url": back, "text": s.tr(r, "office.back")}},
		},
	}
	token, err := s.Office.Sign(cfg)
	if err != nil {
		return "", err
	}
	cfg["token"] = token
	raw, err := json.Marshal(cfg)
	return string(raw), err
}

// officeFS are the empty documents «Создать» starts from (ONLYOFFICE's own, by language: page size, fonts).
//
//go:embed office
var officeFS embed.FS

// officeBlank is the empty document of the kind (docx, xlsx, pptx) in the language.
func officeBlank(lang, kind string) ([]byte, error) {
	if data, err := officeFS.ReadFile("office/" + lang + "/new." + kind); err == nil {
		return data, nil
	}
	return officeFS.ReadFile("office/default/new." + kind)
}

// officeNew makes an empty document (docx, xlsx or pptx) in the folder and opens it in the editor.
func (s *Server) officeNew(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	folder := r.FormValue("folder")
	back := docsBack(r, diskBack(d, folder))
	kind := r.FormValue("kind")
	if s.Office == nil || officeTypes[kind] == "" {
		s.taskErr(w, r, drive.ErrNotFound, back)
		return
	}
	data, err := officeBlank(s.lang(r), kind)
	if err != nil {
		s.fail(w, "office blank", err)
		return
	}
	name := strings.TrimSpace(strings.NewReplacer("/", " ", "\\", " ").Replace(r.FormValue("name")))
	if name == "" {
		name = s.tr(r, "office.new_"+kind)
	}
	if office.Ext(name) != kind {
		name += "." + kind
	}
	if err := s.Drive.CheckFolder(r.Context(), d, folder); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if same, _ := s.Drive.Same(r.Context(), sess.UserID, d, folder, name); same != "" {
		s.taskErr(w, r, i18n.E("office.exists"), back)
		return
	}
	id, err := s.diskStore(r, sess, d, folder, "", name, officeTypes[kind], bytes.NewReader(data), int64(len(data)))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	to := d.Path() + "/edit/" + id + "?folder=" + url.QueryEscape(folder)
	if back == "/docs" {
		to += "&from=docs"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// officeTypes are the content types of the documents go24 makes.
var officeTypes = map[string]string{
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

// officeFile gives the document server the file it opens: the link is signed for this state of the file.
func (s *Server) officeFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.Storages.FileMeta(r.Context(), id)
	if s.Office == nil || err != nil || m.Drive == "" || !s.Office.Stamped(r.PathValue("stamp"), id, m.Key) {
		http.NotFound(w, r)
		return
	}
	var obj io.ReadCloser = io.NopCloser(strings.NewReader(""))
	if m.Size > 0 {
		if obj, err = s.Storages.Open(r.Context(), m.StorageID, m.Key); err != nil {
			s.fail(w, "office file", err)
			return
		}
	}
	defer obj.Close()
	w.Header().Set("Content-Type", m.ContentType)
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("office file", "id", id, "err", err)
	}
}

// officeCallback is what the document server says of a document; when everyone has closed it (or pressed
// «Сохранить»), go24 takes the edited file and keeps it as the file's new version by the user who opened it.
func (s *Server) officeCallback(w http.ResponseWriter, r *http.Request) {
	id, q := r.PathValue("id"), r.URL.Query()
	userID, which := q.Get("u"), q.Get("w")
	if s.Office == nil || !s.Office.Stamped(q.Get("s"), id, userID, which) {
		http.NotFound(w, r)
		return
	}
	var cb office.Callback
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&cb)
	if err == nil {
		err = s.officeVerify(r, &cb)
	}
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": 1})
		return
	}
	switch cb.Status { // who has it open now, for the drive's list
	case office.StatusEditing:
		err = s.Drive.Editing(r.Context(), id, cb.Users)
	case office.StatusSave, office.StatusSaveError, office.StatusClosed, office.StatusForceSaveError:
		err = s.Drive.Editing(r.Context(), id, nil)
	}
	if err != nil {
		s.Log.Error("office editing", "id", id, "err", err)
	}
	if cb.Status == office.StatusSave || cb.Status == office.StatusForceSave {
		if err := s.officeSave(r.Context(), id, userID, which, cb.URL); err != nil {
			s.Log.Error("office save", "id", id, "err", err)
			writeJSON(w, http.StatusOK, map[string]any{"error": 1})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"error": 0})
}

// officeVerify takes the callback from its token (in the body or, without one, in the Authorization header).
func (s *Server) officeVerify(r *http.Request, cb *office.Callback) error {
	if cb.Token != "" {
		return s.Office.Verify(cb.Token, cb)
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return office.ErrToken
	}
	var outer struct {
		Payload office.Callback `json:"payload"`
	}
	if err := s.Office.Verify(token, &outer); err != nil {
		return err
	}
	*cb = outer.Payload
	return nil
}

// officeSave fetches the edited file from the document server and makes it the file's current version.
func (s *Server) officeSave(ctx context.Context, id, userID, which, link string) error {
	var guest, active bool
	if err := s.DB.QueryRow(ctx, `SELECT active, guest FROM users WHERE id::text = $1`, userID).Scan(&active, &guest); err != nil || !active {
		return errors.Join(drive.ErrForbidden, err)
	}
	ctx = tasks.WithViewer(ctx, userID, guest)
	d, err := s.Drive.Open(ctx, userID, which)
	if err != nil {
		return err
	}
	m, err := s.Storages.FileMeta(ctx, id)
	if err != nil {
		return err
	}
	data, err := s.officeDownload(ctx, link)
	if err != nil {
		return err
	}
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), m.Name)
	storageID := ""
	if m.StorageID != nil {
		storageID = *m.StorageID
	}
	if err := s.Storages.Put(ctx, userID, false, storageID, key, bytes.NewReader(data), int64(len(data)), m.ContentType); err != nil {
		if storageID == "" {
			return err
		}
		storageID = "" // the storage is not the user's: the version goes to the built-in one
		if err := s.Storages.Put(ctx, userID, false, "", key, bytes.NewReader(data), int64(len(data)), m.ContentType); err != nil {
			return err
		}
	}
	return s.Drive.AddVersion(ctx, userID, d, id, drive.NewVersion{Key: key, ContentType: m.ContentType, StorageID: storageID, Size: int64(len(data))})
}

// officeDownload fetches the edited file from the document server.
func (s *Server) officeDownload(ctx context.Context, link string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Office.Inside(link), nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("office: the document server gave " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxOfficeFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxOfficeFile {
		return nil, errors.New("office: the edited file is too large")
	}
	return data, nil
}

// officeProxy is the document server under go24's own address (/office/…), over http and https alike: the editor's
// scripts, its connection (WebSocket) and its links. The document server builds its links from X-Forwarded-Host.
func (s *Server) officeProxy() http.Handler {
	target, err := url.Parse(s.Office.URL)
	if err != nil {
		panic(err)
	}
	return &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme, pr.Out.URL.Host = target.Scheme, target.Host
		pr.Out.URL.Path, pr.Out.URL.RawPath = strings.TrimPrefix(pr.In.URL.Path, "/office"), ""
		pr.Out.Host = ""
		pr.Out.Header.Del("Cookie") // go24's session is not the document server's business
		proto := "http"
		if pr.In.TLS != nil || pr.In.Header.Get("X-Forwarded-Proto") == "https" {
			proto = "https"
		}
		pr.Out.Header.Set("X-Forwarded-Proto", proto)
		pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host+"/office")
	}}
}
