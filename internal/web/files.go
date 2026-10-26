package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- files ----------

func (s *Server) files(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	admin := tasks.IsAdmin(r.Context())
	list, err := s.Storages.ListFiles(r.Context(), sess.UserID, admin)
	if err != nil {
		s.taskErr(w, r, err, "/files")
		return
	}
	stores, err := s.Storages.List(r.Context(), sess.UserID, admin)
	if err != nil {
		s.taskErr(w, r, err, "/files")
		return
	}
	s.render(w, r, "files", page{TitleKey: "title.files", Nav: "files", Data: map[string]any{"Files": list, "Storages": stores}})
}

// upload stores a file in the chosen storage; with task=KEY-N it becomes an attachment of a visible task.
func (s *Server) upload(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	back := "/files"
	taskKey := strings.TrimSpace(r.URL.Query().Get("task"))
	if taskKey != "" {
		back = "/t/" + taskKey
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		s.taskErr(w, r, i18n.E("err.file_required"), back)
		return
	}
	defer f.Close()

	var taskID string
	if taskKey != "" {
		if taskID, err = s.Tasks.TaskIDByKey(r.Context(), taskKey); err == nil {
			err = s.Tasks.CheckVisible(r.Context(), sess.UserID, taskID)
		}
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
	}
	if err := s.Tasks.CanAttach(r.Context(), sess.UserID, taskID); err != nil { // a collab's «гости загружают файлы»
		s.taskErr(w, r, err, back)
		return
	}
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
		s.taskErr(w, r, err, back)
		return
	}
	fileID, err := s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: ct, Size: hdr.Size,
		StorageID: storageID, TaskID: taskID, UploadedBy: sess.UserID})
	if err != nil {
		s.fail(w, "save file row", err)
		return
	}
	if taskID != "" {
		s.keepUploaded(r, sess, fileID)
		if err := s.Tasks.FileAdded(r.Context(), sess.UserID, taskID, name); err != nil {
			s.Log.Error("file history", "err", err)
		}
	}
	if taskKey != "" {
		back += "#files"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// fileAccess is a file the user may read: the uploader, anyone who sees the file's task, post, meeting or drive, or an
// administrator in admin mode; a letter's attachment — only the mailbox's owner (and those it is shared with).
func (s *Server) fileAccess(r *http.Request, sess auth.Session, id string) (storages.FileMeta, error) {
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil {
		return m, err
	}
	allowed := tasks.IsAdmin(r.Context()) || (m.UploadedBy != nil && *m.UploadedBy == sess.UserID)
	if !allowed && m.TaskID != nil {
		allowed = s.Tasks.CheckVisible(r.Context(), sess.UserID, *m.TaskID) == nil
	}
	if m.MailID != "" && s.Mail != nil { // not even uploader or admin mode
		allowed = s.Mail.Owns(r.Context(), sess.UserID, m.MailID) || s.Mail.SharedCan(r.Context(), sess.UserID, m.MailID)
	}
	if !allowed && m.Drive != "" && s.Drive != nil {
		allowed = s.Drive.CanReadFile(r.Context(), sess.UserID, id, m.Drive, m.DriveOwner) ||
			(m.Drive == "user" && s.Drive.SharedFileTo(r.Context(), sess.UserID, id)) // in a folder shared with them
	}
	if !allowed && m.KBSpaceID != "" && s.KB != nil { // a picture of a knowledge base's page
		_, err := s.KB.Space(r.Context(), sess.UserID, m.KBSpaceID)
		allowed = err == nil
	}
	if !allowed && m.EventID != "" && s.Calendar != nil { // a file of a meeting (or its comment)
		_, err := s.Calendar.Seen(r.Context(), sess.UserID, m.EventID)
		allowed = err == nil
	}
	if !allowed && m.PostID != nil && s.Feed != nil {
		allowed = s.Feed.CanSee(r.Context(), sess.UserID, *m.PostID) == nil
	}
	if !allowed {
		return m, tasks.ErrForbidden
	}
	return m, nil
}

// download gives the file to those fileAccess lets read it.
func (s *Server) download(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.fileAccess(r, sess, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/files")
		return
	}
	var obj io.ReadCloser
	if m.Size == 0 {
		obj = io.NopCloser(strings.NewReader("")) // empty files (e.g. the portal placeholders) have no stored object
	} else {
		obj, err = s.Storages.Open(r.Context(), m.StorageID, m.Key)
	}
	if err != nil {
		s.fail(w, "open file", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" && (feed.File{ContentType: m.ContentType}).Image() { // pictures shown in the feed
		disposition = "inline"
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'")
		w.Header().Set("Cache-Control", "private, max-age=86400")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename*=UTF-8''%s", disposition, url.PathEscape(m.Name)))
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("download", "key", m.Key, "err", err)
	}
}

// ---------- storages ----------

func (s *Server) storagesPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Storages.List(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()))
	if err != nil {
		s.taskErr(w, r, err, "/settings/storages")
		return
	}
	s.render(w, r, "storages", page{TitleKey: "title.storages", Nav: "storages", Data: map[string]any{
		"Storages": list, "Types": storages.Types, "Clouds": s.Storages.Clouds(), "Enabled": s.Storages.Enabled(),
		"CanShare": sess.AdminActive(), "Redirect": s.absURL(r, storageOAuthPath),
	}})
}

// storageOAuthPath is where a cloud service sends the user back after signing in: the address its application is
// registered with.
const storageOAuthPath = "/settings/storages/oauth"

// storageState is the form of a cloud storage while the user signs in at the service; it goes there and back signed.
type storageState struct {
	User, Type, Name, Root string
	Shared                 bool
	Exp                    int64
}

func (s *Server) storageStateOf(st storageState) string {
	raw, _ := json.Marshal(st)
	p := base64.RawURLEncoding.EncodeToString(raw)
	return p + "." + s.Sessions.Stamp("storage-oauth", p)
}

func (s *Server) storageStateFrom(state, userID string) (storageState, bool) {
	var st storageState
	p, stamp, ok := strings.Cut(state, ".")
	if !ok || !s.Sessions.Stamped(stamp, "storage-oauth", p) {
		return st, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil || json.Unmarshal(raw, &st) != nil || st.User != userID || time.Now().Unix() > st.Exp {
		return st, false
	}
	return st, true
}

// storageSignIn is «Подключить» of a cloud service: the user signs in there and lets go24 in.
func (s *Server) storageSignIn(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	st := storageState{User: sess.UserID, Type: r.FormValue("type"), Name: strings.TrimSpace(r.FormValue("name")),
		Root: r.FormValue("root"), Shared: r.FormValue("shared") == "on", Exp: time.Now().Add(15 * time.Minute).Unix()}
	switch {
	case st.Name == "":
		s.taskErr(w, r, i18n.E("err.storage_name_required"), "/settings/storages")
		return
	case st.Shared && !tasks.IsAdmin(r.Context()):
		s.taskErr(w, r, storages.ErrForbidden, "/settings/storages")
		return
	}
	to, err := s.Storages.SignInURL(st.Type, s.absURL(r, storageOAuthPath), s.storageStateOf(st))
	if err != nil {
		s.taskErr(w, r, err, "/settings/storages")
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// storageOAuth is the way back from the cloud service: the account the user has signed in to becomes the storage.
func (s *Server) storageOAuth(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/settings/storages"
	st, ok := s.storageStateFrom(r.FormValue("state"), sess.UserID)
	if !ok || r.FormValue("code") == "" {
		s.taskErr(w, r, i18n.E("err.storage_signin"), back)
		return
	}
	_, err := s.Storages.CreateCloud(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()),
		storages.Input{Name: st.Name, Type: st.Type, Shared: st.Shared, Root: st.Root}, s.absURL(r, storageOAuthPath), r.FormValue("code"))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("storage connected", "user_id", sess.UserID, "type", st.Type, "shared", st.Shared)
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "storages.connected")), http.StatusSeeOther)
}

func (s *Server) createStorage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/settings/storages"
	if storages.IsCloud(r.FormValue("type")) {
		s.storageSignIn(w, r, sess)
		return
	}
	_, err := s.Storages.Create(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), storages.Input{
		Name: r.FormValue("name"), Type: r.FormValue("type"), Shared: r.FormValue("shared") == "on",
		URL: r.FormValue("url"), Vendor: r.FormValue("vendor"), Host: r.FormValue("host"), Port: r.FormValue("port"),
		Domain: r.FormValue("domain"), User: r.FormValue("user"), Password: r.FormValue("password"),
		Endpoint: r.FormValue("endpoint"), Region: r.FormValue("region"), KeyID: r.FormValue("key_id"), Secret: r.FormValue("secret"),
		Root: r.FormValue("root"),
	})
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("storage connected", "user_id", sess.UserID, "type", r.FormValue("type"), "shared", r.FormValue("shared") == "on")
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "storages.connected")), http.StatusSeeOther)
}

func (s *Server) checkStorage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/settings/storages"
	if err := s.Storages.Check(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "storages.check_ok")), http.StatusSeeOther)
}

func (s *Server) deleteStorage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/settings/storages"
	if err := s.Storages.Delete(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
