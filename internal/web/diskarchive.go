package web

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/storages"
)

// archiveSlots: how many archives are read at once (a ZIP goes through a temporary file).
var archiveSlots = make(chan struct{}, 2)

// fileArchive is an archive of «Диск» seen without downloading it viewer: its folders and files (JSON), and
// with ?path= — that one file of it, downloaded.
func (s *Server) fileArchive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.fileAccess(r, sess, r.PathValue("id"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if err := s.archiveOut(w, r, m); err != nil {
		s.jsonErr(w, r, err)
	}
}

// apiDiskArchive is GET /api/v1/disk/files/{id}/archive: the contents of an archive, ?path= — a file of it.
func (s *Server) apiDiskArchive(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.apiDiskFile(r, p)
	if err != nil {
		return err
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil {
		return err
	}
	return s.archiveOut(w, r, m)
}

// archiveOut writes the contents of the archive m, or the file ?path= of it; nothing is written when it fails.
func (s *Server) archiveOut(w http.ResponseWriter, r *http.Request, m storages.FileMeta) error {
	kind := drive.ArchiveKind(m.Name)
	if kind == "" {
		return drive.ErrArchiveBroken
	}
	if m.Size > drive.ArchiveMax {
		return drive.ErrArchiveBig
	}
	select {
	case archiveSlots <- struct{}{}:
		defer func() { <-archiveSlots }()
	case <-r.Context().Done():
		return r.Context().Err()
	}
	var obj io.ReadCloser = io.NopCloser(strings.NewReader(""))
	if m.Size > 0 {
		var err error
		if obj, err = s.Storages.Open(r.Context(), m.StorageID, m.Key); err != nil {
			return err
		}
	}
	defer obj.Close()
	if !r.URL.Query().Has("path") {
		a, err := drive.ListArchive(kind, obj, m.Size)
		if err != nil {
			return err
		}
		w.Header().Set("Cache-Control", "no-store")
		out := map[string]any{"name": m.Name, "kind": kind, "files": a.Files, "size": a.Size, "more": a.More,
			"summary": s.tr(r, "disk.archive_summary", a.Files, s.sizeText(r, a.Size)), "entries": archiveRows(r, s, a.Entries)}
		if a.More {
			out["more_text"] = s.tr(r, "disk.archive_more", drive.ArchiveShown)
		}
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	return drive.ArchiveFile(kind, obj, m.Size, r.URL.Query().Get("path"), func(e drive.ArchiveEntry, rd io.Reader) error {
		w.Header().Set("Content-Type", "application/octet-stream") // never shown as a page of go24
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", fmt.Sprint(e.Size))
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(e.Name)))
		if _, err := io.Copy(w, rd); err != nil {
			s.Log.Error("archive file", "key", m.Key, "err", err)
		}
		return nil
	})
}

// archiveRows are the entries of an archive with their sizes as text, for the viewer.
func archiveRows(r *http.Request, s *Server, entries []drive.ArchiveEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		row := map[string]any{"path": e.Path, "name": e.Name, "depth": e.Depth, "dir": e.Dir, "size": e.Size, "locked": e.Locked}
		if !e.Dir {
			row["size_text"] = s.sizeText(r, e.Size)
		}
		if !e.Modified.IsZero() {
			row["modified"] = e.Modified
		}
		out = append(out, row)
	}
	return out
}
