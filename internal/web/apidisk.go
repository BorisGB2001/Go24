package web

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Диск» over REST, as disk.storage.*, disk.folder.* and disk.file.* ----------
//
// A drive is named as on the pages: "my", "company", a project's key, "sh:ID" — a folder shared with the key's owner,
// "sf:ID" — a file shared with them. The rights are those of the drive's page (the folders' rights, locks, the
// drive's limit); the trash and new versions work as there.

func (s *Server) apiDiskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/disk/drives", s.api("disk:read", s.apiDiskDrives))
	mux.HandleFunc("GET /api/v1/disk/drives/{drive}", s.api("disk:read", s.apiDiskList))
	mux.HandleFunc("GET /api/v1/disk/drives/{drive}/trash", s.api("disk:read", s.apiDiskTrash))
	mux.HandleFunc("POST /api/v1/disk/drives/{drive}/folders", s.api("disk:write", s.apiDiskFolder))
	mux.HandleFunc("POST /api/v1/disk/drives/{drive}/files", s.api("disk:write", s.apiDiskUpload))
	mux.HandleFunc("PATCH /api/v1/disk/drives/{drive}/folders/{id}", s.api("disk:write", s.apiDiskRename))
	mux.HandleFunc("PATCH /api/v1/disk/drives/{drive}/files/{id}", s.api("disk:write", s.apiDiskRename))
	mux.HandleFunc("DELETE /api/v1/disk/drives/{drive}/folders/{id}", s.api("disk:write", s.apiDiskDelete))
	mux.HandleFunc("DELETE /api/v1/disk/drives/{drive}/files/{id}", s.api("disk:write", s.apiDiskDelete))
	mux.HandleFunc("POST /api/v1/disk/drives/{drive}/files/{id}/lock", s.api("disk:write", s.apiDiskLock))
	mux.HandleFunc("DELETE /api/v1/disk/drives/{drive}/files/{id}/lock", s.api("disk:write", s.apiDiskLock))
	mux.HandleFunc("POST /api/v1/disk/drives/{drive}/move", s.api("disk:write", s.apiDiskMove))
	mux.HandleFunc("POST /api/v1/disk/drives/{drive}/copy", s.api("disk:write", s.apiDiskMove))
	mux.HandleFunc("POST /api/v1/disk/drives/{drive}/restore", s.api("disk:write", s.apiDiskRestore))
	mux.HandleFunc("GET /api/v1/disk/files/{id}", s.api("disk:read", s.apiDiskDownload))
	mux.HandleFunc("GET /api/v1/disk/files/{id}/versions", s.api("disk:read", s.apiDiskVersions))
	mux.HandleFunc("GET /api/v1/disk/docs", s.api("disk:read", s.apiDiskDocs))
	s.apiDiskLinkRoutes(mux)
}

type apiDrive struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // user | company | project
	Name      string `json:"name"`
	Owner     string `json:"owner,omitempty"`  // whose «Мой диск» a shared folder or file is of
	Access    string `json:"access,omitempty"` // read | edit | full: the access to a shared folder or file
	CanWrite  bool   `json:"can_write"`
	CanManage bool   `json:"can_manage"`
}

type apiDiskFolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiDiskFile struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	ContentType string     `json:"content_type"`
	Size        int64      `json:"size"`
	CreatedAt   time.Time  `json:"created_at"`
	UploadedBy  apiUser    `json:"uploaded_by"`
	LockedBy    *apiUser   `json:"locked_by,omitempty"`
	LockedAt    *time.Time `json:"locked_at,omitempty"`
	URL         string     `json:"url"` // the bytes: GET with the same key
}

func apiDiskFileOf(f drive.File) apiDiskFile {
	out := apiDiskFile{ID: f.ID, Name: f.Name, ContentType: f.ContentType, Size: f.Size, CreatedAt: f.CreatedAt,
		UploadedBy: apiUser{f.UploaderID, f.Uploader}, URL: "/api/v1/disk/files/" + f.ID}
	if f.LockerID != "" {
		out.LockedBy, out.LockedAt = &apiUser{f.LockerID, f.Locker}, f.LockedAt
	}
	return out
}

func apiDriveOf(id, name string, d drive.Drive) apiDrive {
	return apiDrive{ID: id, Kind: d.Kind, Name: name, Owner: d.Owner, Access: d.Access, CanWrite: d.CanWrite, CanManage: d.CanManage}
}

// apiDiskDrives: the drives the key's owner has — as /dav/ lists them: «Мой диск», «Общий диск», the projects'
// drives and what is shared with them.
func (s *Server) apiDiskDrives(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, tr := r.Context(), s.apiTr(r)
	out := []apiDrive{}
	for _, which := range []string{"my", "company"} {
		if d, err := s.Drive.Open(ctx, p.UserID, which); err == nil {
			out = append(out, apiDriveOf(which, tr("disk."+which), d))
		}
	}
	projects, err := s.Tasks.ListProjects(ctx, p.UserID)
	if err != nil {
		return err
	}
	for _, pr := range projects {
		if pr.Archived {
			continue
		}
		if d, err := s.Drive.Open(ctx, p.UserID, pr.Key); err == nil {
			out = append(out, apiDriveOf(pr.Key, pr.Name, d))
		}
	}
	shared, err := s.Drive.SharedWithMe(ctx, p.UserID)
	if err != nil {
		return err
	}
	for _, f := range shared {
		which := "sh:" + f.ID
		if f.File {
			which = "sf:" + f.ID
		}
		if d, err := s.Drive.Open(ctx, p.UserID, which); err == nil {
			out = append(out, apiDriveOf(which, f.Name, d))
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiOpenDrive(r *http.Request, p apikeys.Principal) (drive.Drive, error) {
	return s.Drive.Open(r.Context(), p.UserID, r.PathValue("drive"))
}

// apiDiskList is a folder of the drive (?folder=ID, none — the top): its folders, files and the path to it.
func (s *Server) apiDiskList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	folders, files, trail, err := s.Drive.List(r.Context(), d, r.URL.Query().Get("folder"))
	if err != nil {
		return err
	}
	out := struct {
		Path    []apiDiskFolder `json:"path"`
		Folders []apiDiskFolder `json:"folders"`
		Files   []apiDiskFile   `json:"files"`
	}{[]apiDiskFolder{}, []apiDiskFolder{}, []apiDiskFile{}}
	for _, f := range trail {
		out.Path = append(out.Path, apiDiskFolder{f.ID, f.Name})
	}
	for _, f := range folders {
		out.Folders = append(out.Folders, apiDiskFolder{f.ID, f.Name})
	}
	for _, f := range files {
		out.Files = append(out.Files, apiDiskFileOf(f))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiDiskFolder makes a folder: {"name": "…", "parent": "ID"} (no parent — at the top).
func (s *Server) apiDiskFolder(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Name, Parent string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	id, err := s.Drive.CreateFolder(r.Context(), p.UserID, d, in.Parent, in.Name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, apiDiskFolder{id, strings.TrimSpace(in.Name)})
	return nil
}

// apiDiskUpload takes multipart "file" (one or several) into ?folder= (or the form's "folder"); "path" — the folders
// of an uploaded folder, made as needed. A file named as one in the folder becomes its new version.
func (s *Server) apiDiskUpload(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return i18n.E("err.file_required")
	}
	defer r.MultipartForm.RemoveAll()
	folder := r.FormValue("folder")
	if err := s.Drive.CheckFolder(r.Context(), d, folder); err != nil {
		return err
	}
	hdrs := r.MultipartForm.File["file"]
	if len(hdrs) == 0 {
		return i18n.E("err.file_required")
	}
	if dir := r.FormValue("path"); dir != "" {
		if folder, err = s.Drive.EnsurePath(r.Context(), p.UserID, d, folder, dir); err != nil {
			return err
		}
	}
	sess := auth.Session{UserID: p.UserID}
	ids := map[string]bool{}
	for _, hdr := range hdrs {
		id, err := s.diskPut(r, sess, d, folder, r.FormValue("storage"), hdr)
		if err != nil {
			return err
		}
		ids[id] = true
	}
	_, files, _, err := s.Drive.List(r.Context(), d, folder)
	if err != nil {
		return err
	}
	out := []apiDiskFile{}
	for _, f := range files {
		if ids[f.ID] {
			out = append(out, apiDiskFileOf(f))
		}
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}

// apiDiskRename renames a folder or a file: {"name": "…"}.
func (s *Server) apiDiskRename(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Name string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	if apiDiskIsFile(r) {
		err = s.Drive.RenameFile(r.Context(), p.UserID, d, r.PathValue("id"), in.Name)
	} else {
		err = s.Drive.RenameFolder(r.Context(), p.UserID, d, r.PathValue("id"), in.Name)
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiDiskIsFile: the route names a file (…/files/{id}), not a folder.
func apiDiskIsFile(r *http.Request) bool {
	return strings.Contains(r.URL.Path, "/files/")
}

// apiDiskDelete puts a folder (with all it holds) or a file into the drive's trash.
func (s *Server) apiDiskDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	if apiDiskIsFile(r) {
		err = s.Drive.DeleteFile(r.Context(), p.UserID, d, r.PathValue("id"))
	} else {
		err = s.Drive.DeleteFolder(r.Context(), p.UserID, d, r.PathValue("id"))
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiDiskLock: POST locks the file for the others («Заблокировать»), DELETE lifts the lock.
func (s *Server) apiDiskLock(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	if r.Method == http.MethodDelete {
		err = s.Drive.Unlock(r.Context(), p.UserID, d, r.PathValue("id"))
	} else {
		err = s.Drive.Lock(r.Context(), p.UserID, d, r.PathValue("id"))
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiDiskSelection struct {
	Files   []string `json:"files"`
	Folders []string `json:"folders"`
}

// apiDiskMove moves (…/move) or copies (…/copy) files and folders: {"files": [...], "folders": [...], "to": "drive",
// "to_folder": "ID"} — "to" is this drive when left out, "to_folder" the top.
func (s *Server) apiDiskMove(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		apiDiskSelection
		To       string `json:"to"`
		ToFolder string `json:"to_folder"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	dst := d
	if in.To != "" && in.To != r.PathValue("drive") {
		if dst, err = s.Drive.Open(r.Context(), p.UserID, in.To); err != nil {
			return err
		}
	}
	sel := drive.Selection{Files: in.Files, Folders: in.Folders}
	if strings.HasSuffix(r.URL.Path, "/copy") {
		err = s.Drive.Copy(r.Context(), p.UserID, d, sel, dst, in.ToFolder, s.Storages.Copy)
	} else {
		err = s.Drive.Move(r.Context(), p.UserID, d, sel, dst, in.ToFolder)
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiTrashed struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"` // file | folder
	Name      string     `json:"name"`
	Where     string     `json:"where"` // the folder it was in ("" — the top)
	Size      int64      `json:"size"`
	DeletedBy apiUser    `json:"deleted_by"`
	DeletedAt time.Time  `json:"deleted_at"`
	PurgeAt   *time.Time `json:"purge_at,omitempty"` // when it goes for good
}

// apiDiskTrash lists the drive's trash.
func (s *Server) apiDiskTrash(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	list, err := s.Drive.Trash(r.Context(), d)
	if err != nil {
		return err
	}
	out := []apiTrashed{}
	for _, t := range list {
		out = append(out, apiTrashed{t.ID, t.Kind, t.Name, t.Where, t.Size, apiUser{t.DeletedByID, t.DeletedBy}, t.DeletedAt, t.PurgeAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiDiskRestore takes files and folders out of the drive's trash: {"files": [...], "folders": [...]}.
func (s *Server) apiDiskRestore(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in apiDiskSelection
	if err := readJSON(r, &in); err != nil {
		return err
	}
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	if err := s.Drive.Restore(r.Context(), p.UserID, d, drive.Selection{Files: in.Files, Folders: in.Folders}); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiDiskFile is a file of a drive the key's owner may read.
func (s *Server) apiDiskFile(r *http.Request, p apikeys.Principal) (string, error) {
	id := r.PathValue("id")
	m, err := s.fileAccess(r, auth.Session{UserID: p.UserID}, id)
	if err != nil {
		return "", err
	}
	if m.Drive == "" {
		return "", tasks.ErrNotFound // an attachment, not a file of a drive
	}
	return id, nil
}

// apiDiskDownload gives the file's bytes.
func (s *Server) apiDiskDownload(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.apiDiskFile(r, p)
	if err != nil {
		return err
	}
	return s.apiSendFile(w, r, id)
}

// apiSendFile gives a stored file's bytes as an attachment (the caller has checked the access).
func (s *Server) apiSendFile(w http.ResponseWriter, r *http.Request, id string) error {
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil {
		return err
	}
	var obj io.ReadCloser = io.NopCloser(strings.NewReader(""))
	if m.Size > 0 {
		if obj, err = s.Storages.Open(r.Context(), m.StorageID, m.Key); err != nil {
			return err
		}
	}
	defer obj.Close()
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("Content-Length", fmt.Sprint(m.Size))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(m.Name)))
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("api file download", "err", err)
	}
	return nil
}

type apiVersion struct {
	ID          string    `json:"id"`
	No          int       `json:"no"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	Author      apiUser   `json:"author"`
	CreatedAt   time.Time `json:"created_at"`
}

// apiDiskVersions is the file's history, the current version first.
func (s *Server) apiDiskVersions(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.apiDiskFile(r, p)
	if err != nil {
		return err
	}
	list, err := s.Drive.Versions(r.Context(), id)
	if err != nil {
		return err
	}
	out := []apiVersion{}
	for _, v := range list {
		out = append(out, apiVersion{v.ID, v.No, v.Size, v.ContentType, apiUser{v.AuthorID, v.Author}, v.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
