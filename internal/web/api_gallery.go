package web

import (
	"net/http"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// REST of «Фотогалерея» (GO24DEV-929), with the rights of the pages:
//
//	GET    /api/v1/gallery/albums?parent=        — the albums in an album ("" — at the top) the key owner sees
//	POST   /api/v1/gallery/albums                — {"parent_id","title","description","anyone_adds","access":["ID","dept:ID"]}
//	GET    /api/v1/gallery/albums/{id}           — the album
//	PUT    /api/v1/gallery/albums/{id}           — the same fields as POST
//	DELETE /api/v1/gallery/albums/{id}           — with the albums and photos in it
//	GET    /api/v1/gallery/albums/{id}/photos    — its photos
//	POST   /api/v1/gallery/albums/{id}/photos    — multipart files (up to 50), caption
//	GET    /api/v1/gallery/photos/{id}/file      — the picture
//	DELETE /api/v1/gallery/photos/{id}

func (s *Server) apiGalleryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/gallery/albums", s.api("company:read", s.apiAlbums))
	mux.HandleFunc("POST /api/v1/gallery/albums", s.api("company:write", s.apiAlbumSave))
	mux.HandleFunc("GET /api/v1/gallery/albums/{id}", s.api("company:read", s.apiAlbum))
	mux.HandleFunc("PUT /api/v1/gallery/albums/{id}", s.api("company:write", s.apiAlbumSave))
	mux.HandleFunc("DELETE /api/v1/gallery/albums/{id}", s.api("company:write", s.apiAlbumDelete))
	mux.HandleFunc("GET /api/v1/gallery/albums/{id}/photos", s.api("company:read", s.apiPhotos))
	mux.HandleFunc("POST /api/v1/gallery/albums/{id}/photos", s.api("company:write", s.apiPhotosAdd))
	mux.HandleFunc("GET /api/v1/gallery/photos/{id}/file", s.api("company:read", s.apiPhotoFile))
	mux.HandleFunc("DELETE /api/v1/gallery/photos/{id}", s.api("company:write", s.apiPhotoDelete))
}

func (s *Server) apiAlbums(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sess := s.apiSess(r, p)
	parent := r.URL.Query().Get("parent")
	keeper, err := s.galleryKeeper(r.Context(), sess)
	if err == nil && parent != "" {
		_, keeper, err = s.seeAlbum(r.Context(), sess, parent)
	}
	if err != nil {
		return err
	}
	list, err := s.Company.Albums(r.Context(), p.UserID, parent, keeper)
	if err != nil {
		return err
	}
	if list == nil {
		list = []company.Album{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) apiAlbum(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, _, err := s.seeAlbum(r.Context(), s.apiSess(r, p), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

func (s *Server) apiAlbumSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		ParentID    string   `json:"parent_id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		AnyoneAdds  bool     `json:"anyone_adds"`
		Access      []string `json:"access"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	a := company.Album{ID: r.PathValue("id"), ParentID: in.ParentID, Title: in.Title, Description: in.Description,
		AnyoneAdds: in.AnyoneAdds, CreatedBy: p.UserID}
	if err := s.galleryMayWrite(r.Context(), s.apiSess(r, p), a); err != nil {
		return err
	}
	id, err := s.Company.SaveAlbum(r.Context(), a)
	if err == nil {
		err = s.Company.SetAlbumAccess(r.Context(), id, in.Access)
	}
	if err != nil {
		return err
	}
	if a, err = s.Company.Album(r.Context(), id); err != nil {
		return err
	}
	st := http.StatusOK
	if r.Method == http.MethodPost {
		st = http.StatusCreated
	}
	writeJSON(w, st, a)
	return nil
}

func (s *Server) apiAlbumDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, keeper, err := s.seeAlbum(r.Context(), s.apiSess(r, p), r.PathValue("id"))
	if err != nil {
		return err
	}
	if !a.CanChange(p.UserID, keeper) {
		return company.ErrForbidden
	}
	if err := s.Company.DeleteAlbum(r.Context(), a.ID); err != nil {
		return err
	}
	s.Log.Info("api album deleted", "user_id", p.UserID, "id", a.ID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) apiPhotos(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, _, err := s.seeAlbum(r.Context(), s.apiSess(r, p), r.PathValue("id"))
	if err != nil {
		return err
	}
	list, err := s.Company.Photos(r.Context(), p.UserID, a.ID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []company.Photo{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) apiPhotosAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxGalleryForm)
	a, keeper, err := s.seeAlbum(r.Context(), s.apiSess(r, p), r.PathValue("id"))
	if err != nil {
		return err
	}
	if !a.CanAdd(p.UserID, keeper) {
		return company.ErrForbidden
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return i18n.E("err.photo_file")
	}
	defer r.MultipartForm.RemoveAll()
	n, err := s.addPhotos(r.Context(), p.UserID, a.ID, r.MultipartForm.File["files"], r.FormValue("caption"))
	if n > 0 {
		s.tellGallery(r, auth.Session{UserID: p.UserID}, a, n)
	}
	if err != nil {
		return err
	}
	list, err := s.Company.Photos(r.Context(), p.UserID, a.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, list[max(0, len(list)-n):])
	return nil
}

// apiPhoto is the photo of an album the key owner sees, with the album.
func (s *Server) apiPhoto(r *http.Request, p apikeys.Principal) (company.Photo, company.Album, bool, error) {
	ph, err := s.Company.Photo(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return ph, company.Album{}, false, err
	}
	a, keeper, err := s.seeAlbum(r.Context(), s.apiSess(r, p), ph.AlbumID)
	return ph, a, keeper, err
}

func (s *Server) apiPhotoFile(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ph, _, _, err := s.apiPhoto(r, p)
	if err != nil {
		return err
	}
	s.servePhoto(w, r, ph)
	return nil
}

func (s *Server) apiPhotoDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ph, a, keeper, err := s.apiPhoto(r, p)
	if err != nil {
		return err
	}
	if !a.CanChange(p.UserID, keeper) && ph.CreatedBy != p.UserID {
		return company.ErrForbidden
	}
	if err := s.Company.DeletePhoto(r.Context(), ph.ID); err != nil {
		return err
	}
	s.Log.Info("api photo deleted", "user_id", p.UserID, "id", ph.ID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
