package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/mail"
	"go24/internal/office"
	"go24/internal/storages"
	"go24/internal/tasks"
	"go24/internal/thumb"
)

// thumbSlots: how many thumbnails are made at once (a folder of photos opened in tiles asks for all of them).
var thumbSlots = make(chan struct{}, 3)

// fileThumb is the thumbnail of a file for the tiles of «Диск», as the previews of the Disk: a picture made
// smaller, the first page of a PDF or of an office document (by the document server). It is made the first time and
// kept for the file's state; a picture the server cannot make smaller (WebP, SVG) is shown itself, of other files
// with no thumbnail — 404, and the page shows the file's icon.
func (s *Server) fileThumb(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	m, err := s.fileAccess(r, sess, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := s.thumbOf(r, id, m)
	if data == nil {
		if fileKind(m.Name, m.ContentType) == mail.PreviewImage {
			http.Redirect(w, r, "/files/"+id+"/raw", http.StatusSeeOther)
			return
		}
		http.NotFound(w, r)
		return
	}
	sendThumb(w, r, m, data)
}

// apiDiskThumb is the thumbnail of a file of a drive, as PREVIEW_URL of disk.file.get: 404 — none.
func (s *Server) apiDiskThumb(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.apiDiskFile(r, p)
	if err != nil {
		return err
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil {
		return err
	}
	data := s.thumbOf(r, id, m)
	if data == nil {
		return tasks.ErrNotFound
	}
	sendThumb(w, r, m, data)
	return nil
}

// thumbOf is the thumbnail of the file's state m, kept or made now; nil — none.
func (s *Server) thumbOf(r *http.Request, id string, m storages.FileMeta) []byte {
	data, found, err := s.Drive.Thumb(r.Context(), id, m.Key)
	if err == nil && !found {
		data, err = s.makeThumb(r, id, m)
		switch {
		case err == nil, errors.Is(err, thumb.ErrNone):
			err = s.Drive.SetThumb(r.Context(), id, m.Key, data)
		default:
			s.Log.Error("file thumb", "file", id, "err", err) // the document server or the storage: tried again next time
			return nil
		}
	}
	if err != nil {
		s.Log.Error("file thumb", "file", id, "err", err)
	}
	return data
}

// sendThumb gives a thumbnail (JPEG); its ETag is of the file's state, so a new version is seen at once.
func sendThumb(w http.ResponseWriter, r *http.Request, m storages.FileMeta, data []byte) {
	sum := sha256.Sum256([]byte(m.Key))
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// makeThumb makes the thumbnail of the file's state m; thumb.ErrNone — none can be made of it.
func (s *Server) makeThumb(r *http.Request, id string, m storages.FileMeta) ([]byte, error) {
	kind := fileKind(m.Name, m.ContentType)
	doc := kind != mail.PreviewImage && kind != mail.PreviewPDF
	if !hasThumb(m.Name, m.ContentType) || (doc && s.Office == nil) || m.Size == 0 {
		return nil, thumb.ErrNone
	}
	select {
	case thumbSlots <- struct{}{}:
		defer func() { <-thumbSlots }()
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	if doc {
		return s.officeThumb(r, id, m)
	}
	data, err := s.readFile(r, m, 50<<20)
	if err != nil {
		return nil, thumb.ErrNone // too big
	}
	if kind == mail.PreviewPDF {
		return thumb.PDF(r.Context(), data)
	}
	return thumb.Image(data)
}

// officeThumb has the document server draw the first page of an office document.
func (s *Server) officeThumb(r *http.Request, id string, m storages.FileMeta) ([]byte, error) {
	link, err := s.Office.Thumbnail(r.Context(), s.Office.Self+"/onlyoffice/convert/"+id+"/"+s.Office.Stamp("convert", id, m.Key),
		office.Ext(m.Name), office.Key(id, m.Key), m.Name, 2*thumb.Side)
	if errors.Is(err, office.ErrConvert) {
		return nil, thumb.ErrNone // a file the document server cannot read
	}
	if err != nil {
		return nil, err
	}
	data, err := officeFetch(r, link)
	if err != nil {
		return nil, err
	}
	return thumb.Image(data)
}

// hasThumb: the tiles ask for the file's thumbnail (/files/{id}/thumb) — a picture, a PDF, an office document.
func hasThumb(name, contentType string) bool {
	kind := fileKind(name, contentType)
	return kind == mail.PreviewImage || kind == mail.PreviewPDF || (kind != "video" && kind != "audio" && office.Kind(name) != "")
}
