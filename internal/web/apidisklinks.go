package web

import (
	"net/http"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/drive"
	"go24/internal/i18n"
)

// ---------- «Диск» over REST: public links, «Общий доступ», versions brought back and «Обсуждение» — as
// disk.file.getExternalLink, disk.folder.sharetouser and disk.file.restoreFromVersion ----------

func (s *Server) apiDiskLinkRoutes(mux *http.ServeMux) {
	for _, kind := range []string{"files", "folders"} {
		base := "/api/v1/disk/drives/{drive}/" + kind + "/{id}"
		mux.HandleFunc("GET "+base+"/link", s.api("disk:read", s.apiDiskLink))
		mux.HandleFunc("PUT "+base+"/link", s.api("disk:write", s.apiDiskLink))
		mux.HandleFunc("DELETE "+base+"/link", s.api("disk:write", s.apiDiskLink))
		mux.HandleFunc("GET "+base+"/shares", s.api("disk:read", s.apiDiskShares))
		mux.HandleFunc("POST "+base+"/shares", s.api("disk:write", s.apiDiskShares))
	}
	mux.HandleFunc("DELETE /api/v1/disk/drives/{drive}/shares/{share}", s.api("disk:write", s.apiDiskUnshare))
	mux.HandleFunc("POST /api/v1/disk/drives/{drive}/files/{id}/versions", s.api("disk:write", s.apiDiskVersionUpload))
	mux.HandleFunc("POST /api/v1/disk/drives/{drive}/files/{id}/versions/{version}/restore", s.api("disk:write", s.apiDiskVersionRestore))
	mux.HandleFunc("GET /api/v1/disk/files/{id}/comments", s.api("disk:read", s.apiDiskComments))
	mux.HandleFunc("POST /api/v1/disk/files/{id}/comments", s.api("disk:write", s.apiDiskComments))
	mux.HandleFunc("DELETE /api/v1/disk/files/{id}/comments/{comment}", s.api("disk:write", s.apiDiskUncomment))
}

type apiDiskLinkOut struct {
	URL       string     `json:"url"` // /s/{token}: for anyone, no sign-in
	Password  bool       `json:"password"`
	ExpiresAt *time.Time `json:"expires_at"`
	Edit      bool       `json:"edit"` // a document edited by those who open it
	Downloads int        `json:"downloads"`
	CreatedAt time.Time  `json:"created_at"`
}

// apiDiskKind is the route's thing: "file" (…/files/{id}/…) or "folder".
func apiDiskKind(r *http.Request) string {
	if apiDiskIsFile(r) {
		return "file"
	}
	return "folder"
}

// apiDiskLink is the public link of a file or a folder, for those who write in the drive: GET — it (404 — none);
// PUT {"password": "…", "clear_password": true, "expires": "2026-12-31" (a date — through that day, or a time),
// "edit": true} makes or changes it; DELETE switches it off.
func (s *Server) apiDiskLink(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	ctx, kind, id := r.Context(), apiDiskKind(r), r.PathValue("id")
	switch r.Method {
	case http.MethodDelete:
		if err := s.Drive.DeleteLink(ctx, p.UserID, d, kind, id); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	case http.MethodPut:
		var in struct {
			Password      string `json:"password"`
			ClearPassword bool   `json:"clear_password"`
			Expires       string `json:"expires"`
			Edit          bool   `json:"edit"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		var expires *time.Time
		if in.Expires != "" {
			at, err := time.Parse(time.RFC3339, in.Expires)
			if err != nil {
				day, derr := time.ParseInLocation("2006-01-02", in.Expires, time.Local)
				if derr != nil {
					return i18n.E("err.link_expired_date")
				}
				at = day.AddDate(0, 0, 1).Add(-time.Second)
			}
			expires = &at
		}
		if _, err := s.Drive.SetLink(ctx, p.UserID, d, kind, id, in.Password, in.ClearPassword, expires, in.Edit, r.PathValue("drive")); err != nil {
			return err
		}
	}
	l, err := s.Drive.LinkOf(ctx, d, kind, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiDiskLinkOut{s.absURL(r, "/s/"+l.Token), l.Password, l.ExpiresAt, l.Edit, l.Downloads, l.CreatedAt})
	return nil
}

type apiDiskShare struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"` // user | department
	Whom   apiUser `json:"whom"`
	Access string  `json:"access"` // read | edit | full (a file: read | edit)
}

// apiDiskShares is «Общий доступ» of a folder or a file of one's own «Мой диск» (the drive "my"): GET — whom it is
// open to; POST {"whom": "user:ID" | "department:ID", "access": "read"} opens it to them or changes their access.
func (s *Server) apiDiskShares(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	folder, file := r.PathValue("id"), ""
	if apiDiskIsFile(r) {
		folder, file = "", folder
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		var in struct{ Whom, Access string }
		if err := readJSON(r, &in); err != nil {
			return err
		}
		kind, whom, _ := strings.Cut(in.Whom, ":")
		if err := s.Drive.SetShare(ctx, p.UserID, d, folder, file, kind, whom, in.Access); err != nil {
			return err
		}
	}
	list, err := s.Drive.SharesOf(ctx, p.UserID, d, folder, file)
	if err != nil {
		return err
	}
	out := []apiDiskShare{}
	for _, sh := range list {
		out = append(out, apiDiskShare{sh.ID, sh.Kind, apiUser{sh.WhomID, sh.Whom}, sh.Access})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiDiskUnshare closes a folder or a file of one's «Мой диск» to the one it was shared with (the share's id).
func (s *Server) apiDiskUnshare(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	if err := s.Drive.DeleteShare(r.Context(), p.UserID, d, r.PathValue("share")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiDiskVersionRestore brings a version of the file back as its current one (the current one goes to the history).
func (s *Server) apiDiskVersionRestore(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	if err := s.Drive.RestoreVersion(r.Context(), p.UserID, d, r.PathValue("id"), r.PathValue("version")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiDiskVersionUpload is disk.file.uploadversion: the uploaded bytes (multipart field file) become the
// file's new version, its name and links stay; the answer is the file.
func (s *Server) apiDiskVersionUpload(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	d, err := s.apiOpenDrive(r, p)
	if err != nil {
		return err
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return i18n.E("err.file_required")
	}
	defer r.MultipartForm.RemoveAll()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return i18n.E("err.file_required")
	}
	defer f.Close()
	id := r.PathValue("id")
	if err := s.diskVersion(r.Context(), p.UserID, d, id, f, hdr.Size); err != nil {
		return err
	}
	on, err := s.Drive.FileOn(r.Context(), d, id)
	if err != nil {
		return err
	}
	_, files, _, err := s.Drive.List(r.Context(), d, on.FolderID)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.ID == id {
			writeJSON(w, http.StatusOK, apiDiskFileOf(f))
			return nil
		}
	}
	return drive.ErrNotFound
}

type apiDiskComment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	Author    apiUser   `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// apiDiskComments is «Обсуждение» of a file the key's owner may read: GET — the comments, oldest first; POST
// {"body": "…"} adds one (the uploader and those who commented hear of it).
func (s *Server) apiDiskComments(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.apiDiskFile(r, p)
	if err != nil {
		return err
	}
	if r.Method == http.MethodPost {
		var in struct{ Body string }
		if err := readJSON(r, &in); err != nil {
			return err
		}
		cid, err := s.Drive.AddComment(r.Context(), p.UserID, id, in.Body)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, map[string]string{"id": cid})
		return nil
	}
	c, err := s.Drive.FileCard(r.Context(), id)
	if err != nil {
		return err
	}
	out := []apiDiskComment{}
	for _, cm := range c.Comments {
		out = append(out, apiDiskComment{cm.ID, cm.Body, apiUser{cm.AuthorID, cm.Author}, cm.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiDiskUncomment deletes a comment of the file: its author does, or an administrator.
func (s *Server) apiDiskUncomment(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.apiDiskFile(r, p)
	if err != nil {
		return err
	}
	if err := s.Drive.DeleteComment(r.Context(), p.UserID, id, r.PathValue("comment")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
