package web

import (
	"bytes"
	"context"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/thumb"
)

// «Фотогалерея» of the company (GO24DEV-929): albums and albums in them, photos uploaded in a batch,
// a photo's page with its neighbours, a slide show, «Нравится» and comments; new photos are told in the feed to
// those who see the album.

const (
	galleryBase     = "/company/gallery"
	photoBase       = "/company/photos"
	maxGallerySize  = 20 << 20 // a photo, as a phone takes it
	maxPhotoBatch   = 50       // photos in one upload
	maxGalleryForm  = maxPhotoBatch*maxGallerySize + 1<<20
	galleryMaxPhoto = 40 << 20 // pixels of a photo whose size is read
)

// galleryTypes are the pictures the gallery keeps; a WEBP has no thumbnail of its own — it is shown itself.
var galleryTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}

func (s *Server) galleryKeeper(ctx context.Context, sess auth.Session) (bool, error) {
	return s.Company.CanKeepGallery(ctx, sess.UserID, sess.Admin)
}

// seeAlbum is the album the viewer sees (ErrNotFound — none, or closed to them), whether they keep the gallery.
func (s *Server) seeAlbum(ctx context.Context, sess auth.Session, id string) (company.Album, bool, error) {
	keeper, err := s.galleryKeeper(ctx, sess)
	if err != nil {
		return company.Album{}, false, err
	}
	ok, err := s.Company.SeesAlbum(ctx, sess.UserID, id, keeper)
	if err == nil && !ok {
		err = company.ErrNotFound // a closed album is not even there
	}
	if err != nil {
		return company.Album{}, false, err
	}
	a, err := s.Company.Album(ctx, id)
	return a, keeper, err
}

// galleryAlbum is seeAlbum, or the error page is shown.
func (s *Server) galleryAlbum(w http.ResponseWriter, r *http.Request, sess auth.Session, id string) (company.Album, bool, bool) {
	a, keeper, err := s.seeAlbum(r.Context(), sess, id)
	if err != nil {
		s.taskErr(w, r, err, galleryBase)
		return a, false, false
	}
	return a, keeper, true
}

// galleryPhoto is the photo of an album the viewer sees, with the album.
func (s *Server) galleryPhoto(w http.ResponseWriter, r *http.Request, sess auth.Session) (company.Photo, company.Album, bool, bool) {
	p, err := s.Company.Photo(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, galleryBase)
		return p, company.Album{}, false, false
	}
	a, keeper, ok := s.galleryAlbum(w, r, sess, p.AlbumID)
	return p, a, keeper, ok
}

// galleryPage: GET /company/gallery — the albums at the top.
func (s *Server) galleryPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	keeper, err := s.galleryKeeper(r.Context(), sess)
	var albums []company.Album
	if err == nil {
		albums, err = s.Company.Albums(r.Context(), sess.UserID, "", keeper)
	}
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	s.render(w, r, "gallery", page{TitleKey: "gallery.title", Nav: "gallery", Data: map[string]any{
		"Albums": albums, "CanNew": keeper, "TitleMax": company.AlbumTitleMax, "DescMax": company.AlbumDescMax}})
}

// galleryAlbumPage: GET /company/gallery/{id} — the album: the albums in it, its photos, upload, «Настройки альбома».
func (s *Server) galleryAlbumPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	a, keeper, ok := s.galleryAlbum(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	pathTo, err := s.Company.AlbumPath(r.Context(), a.ID)
	var albums []company.Album
	var photos []company.Photo
	if err == nil {
		albums, err = s.Company.Albums(r.Context(), sess.UserID, a.ID, keeper)
	}
	if err == nil {
		photos, err = s.Company.Photos(r.Context(), sess.UserID, a.ID)
	}
	data := map[string]any{"A": a, "Path": pathTo[:len(pathTo)-1], "Albums": albums, "Photos": photos,
		"CanChange": a.CanChange(sess.UserID, keeper), "CanAdd": a.CanAdd(sess.UserID, keeper), "Keeper": keeper,
		"TitleMax": company.AlbumTitleMax, "DescMax": company.AlbumDescMax, "Batch": maxPhotoBatch, "Added": r.URL.Query().Get("added")}
	if err == nil && a.CanChange(sess.UserID, keeper) {
		var refs []company.DeptRef
		if refs, err = s.Company.AlbumAccess(r.Context(), a.ID); err == nil {
			var chips []tasks.User
			for _, ref := range refs {
				chips = append(chips, tasks.User{ID: ref.ID, Name: ref.Name})
			}
			data["Access"] = chips
		}
	}
	if err != nil {
		s.taskErr(w, r, err, galleryBase)
		return
	}
	s.render(w, r, "galleryalbum", page{Title: a.Title, Nav: "gallery", Data: data})
}

// galleryAlbumSave: POST /company/gallery (a new album: parent, title, description, anyone_adds, access) or
// /company/gallery/{id} (changes it). At the top — the keepers; in an album — who changes that album.
func (s *Server) galleryAlbumSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_ = r.ParseForm()
	a := company.Album{ID: r.PathValue("id"), ParentID: r.FormValue("parent"), Title: r.FormValue("title"),
		Description: r.FormValue("description"), AnyoneAdds: r.FormValue("anyone_adds") == "1", CreatedBy: sess.UserID}
	back := galleryBase
	err := s.galleryMayWrite(r.Context(), sess, a)
	if a.ID != "" {
		back += "/" + a.ID
	} else if a.ParentID != "" {
		back += "/" + a.ParentID
	}
	id := a.ID
	if err == nil {
		id, err = s.Company.SaveAlbum(r.Context(), a)
	}
	if err == nil {
		err = s.Company.SetAlbumAccess(r.Context(), id, r.Form["access"])
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("album saved", "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, galleryBase+"/"+id, http.StatusSeeOther)
}

// galleryMayWrite: the viewer may save the album a (a new one in a.ParentID, or a.ID changed).
func (s *Server) galleryMayWrite(ctx context.Context, sess auth.Session, a company.Album) error {
	keeper, err := s.galleryKeeper(ctx, sess)
	if err != nil {
		return err
	}
	of := a.ID
	if of == "" {
		if a.ParentID == "" {
			if !keeper {
				return company.ErrForbidden
			}
			return nil
		}
		of = a.ParentID
	}
	ok, err := s.Company.SeesAlbum(ctx, sess.UserID, of, keeper)
	if err != nil {
		return err
	}
	if !ok {
		return company.ErrNotFound
	}
	album, err := s.Company.Album(ctx, of)
	if err != nil {
		return err
	}
	if !album.CanChange(sess.UserID, keeper) {
		return company.ErrForbidden
	}
	return nil
}

// galleryAlbumDelete: POST /company/gallery/{id}/delete — the album with its albums and photos.
func (s *Server) galleryAlbumDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	a, keeper, ok := s.galleryAlbum(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	back := galleryBase
	if a.ParentID != "" {
		back += "/" + a.ParentID
	}
	var err error = company.ErrForbidden
	if a.CanChange(sess.UserID, keeper) {
		err = s.Company.DeleteAlbum(r.Context(), a.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, galleryBase+"/"+a.ID)
		return
	}
	s.Log.Info("album deleted", "user_id", sess.UserID, "id", a.ID)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// galleryUpload: POST /company/gallery/{id}/photos (files, caption) — photos in a batch; the feed is told.
func (s *Server) galleryUpload(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxGalleryForm)
	a, keeper, ok := s.galleryAlbum(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	back := galleryBase + "/" + a.ID
	if !a.CanAdd(sess.UserID, keeper) {
		s.taskErr(w, r, company.ErrForbidden, back)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.taskErr(w, r, i18n.E("err.photo_file"), back)
		return
	}
	defer r.MultipartForm.RemoveAll()
	n, err := s.addPhotos(r.Context(), sess.UserID, a.ID, r.MultipartForm.File["files"], r.FormValue("caption"))
	if n > 0 {
		s.tellGallery(r, sess, a, n)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("photos added", "user_id", sess.UserID, "album", a.ID, "n", n)
	http.Redirect(w, r, back+"?added="+strconv.Itoa(n), http.StatusSeeOther)
}

// addPhotos stores the pictures in the album; returns how many were added before an error.
func (s *Server) addPhotos(ctx context.Context, actor, albumID string, files []*multipart.FileHeader, caption string) (int, error) {
	if len(files) == 0 || len(files) > maxPhotoBatch {
		return 0, i18n.E("err.photo_file")
	}
	n := 0
	for _, hdr := range files {
		if _, err := s.addPhoto(ctx, actor, albumID, hdr, caption); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// addPhoto checks the file is a picture of an allowed size, stores it with its thumbnail and keeps it in the album.
func (s *Server) addPhoto(ctx context.Context, actor, albumID string, hdr *multipart.FileHeader, caption string) (string, error) {
	if hdr.Size == 0 || hdr.Size > maxGallerySize {
		return "", i18n.E("err.photo_file")
	}
	f, err := hdr.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxGallerySize+1))
	if err != nil {
		return "", err
	}
	mime := http.DetectContentType(data)
	ext, ok := galleryTypes[mime]
	if !ok || int64(len(data)) > maxGallerySize {
		return "", i18n.E("err.photo_file")
	}
	p := company.Photo{AlbumID: albumID, FileName: path.Base(hdr.Filename), Mime: mime, Size: int64(len(data)),
		Caption: caption, CreatedBy: actor}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && int64(cfg.Width)*int64(cfg.Height) <= galleryMaxPhoto {
		p.Width, p.Height = cfg.Width, cfg.Height
	}
	base := "go24/gallery/" + albumID + "/" + randomHex(8)
	p.FileKey = base + ext
	if err := s.Storages.Put(ctx, actor, false, "", p.FileKey, bytes.NewReader(data), p.Size, mime); err != nil {
		return "", err
	}
	if small, err := thumb.Image(data); err == nil {
		p.ThumbKey = base + "-thumb.jpg"
		if err := s.Storages.Put(ctx, actor, false, "", p.ThumbKey, bytes.NewReader(small), int64(len(small)), "image/jpeg"); err != nil {
			return "", err
		}
	}
	return s.Company.AddPhoto(ctx, p)
}

// tellGallery posts the new photos to the feed for those who see the album.
func (s *Server) tellGallery(r *http.Request, sess auth.Session, a company.Album, n int) {
	users, depts, err := s.Company.AlbumAudience(r.Context(), a.ID)
	if err == nil {
		lang := s.lang(r)
		body := s.I18n.T(lang, "gallery.post", n, a.Title) + "\n[url=" + galleryBase + "/" + a.ID + "]" +
			s.I18n.T(lang, "gallery.open_album") + "[/url]"
		_, err = s.Feed.CreatePostTo(r.Context(), sess.UserID, "", feed.Audience{Users: users, Depts: depts}, "", body, false)
	}
	if err != nil {
		s.Log.Error("gallery post", "album", a.ID, "err", err)
	}
}

// galleryPhotoPage: GET /company/photos/{id} — the photo with its neighbours, likes and comments;
// ?play=1 — the slide show goes on to the next one.
func (s *Server) galleryPhotoPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, a, keeper, ok := s.galleryPhoto(w, r, sess)
	if !ok {
		return
	}
	photos, err := s.Company.Photos(r.Context(), sess.UserID, a.ID)
	var comments []company.PhotoComment
	var likers []company.Person
	var albums []company.Album
	if err == nil {
		comments, err = s.Company.PhotoComments(r.Context(), p.ID)
	}
	if err == nil {
		likers, err = s.Company.Likers(r.Context(), p.ID)
	}
	canChange := a.CanChange(sess.UserID, keeper) || (p.CreatedBy == sess.UserID && sess.UserID != "")
	if err == nil && canChange {
		albums, err = s.galleryTargets(r.Context(), sess, keeper)
	}
	if err != nil {
		s.taskErr(w, r, err, galleryBase+"/"+a.ID)
		return
	}
	data := map[string]any{"P": p, "A": a, "Comments": comments, "Likers": likers, "CanChange": canChange,
		"CanCover": a.CanChange(sess.UserID, keeper), "Keeper": keeper, "Albums": albums, "Play": r.URL.Query().Get("play") == "1",
		"CaptionMax": company.PhotoCaptionMax, "CommentMax": company.PhotoCommentMax, "Count": len(photos)}
	for i, x := range photos {
		if x.ID != p.ID {
			continue
		}
		data["N"] = i + 1
		if i > 0 {
			data["Prev"] = photos[i-1].ID
		}
		if i+1 < len(photos) {
			data["Next"] = photos[i+1].ID
		} else if len(photos) > 1 {
			data["First"] = photos[0].ID // the slide show goes round
		}
	}
	title := p.Caption
	if title == "" {
		title = a.Title
	}
	s.render(w, r, "galleryphoto", page{Title: title, Nav: "gallery", Data: data})
}

// galleryTargets are the albums a photo may be moved to: all the viewer sees and may change, by their paths.
func (s *Server) galleryTargets(ctx context.Context, sess auth.Session, keeper bool) ([]company.Album, error) {
	var out []company.Album
	var walk func(parent, prefix string, depth int) error
	walk = func(parent, prefix string, depth int) error {
		list, err := s.Company.Albums(ctx, sess.UserID, parent, keeper)
		if err != nil || depth > 10 {
			return err
		}
		for _, a := range list {
			title := prefix + a.Title
			if a.CanChange(sess.UserID, keeper) || a.AnyoneAdds {
				x := a
				x.Title = title
				out = append(out, x)
			}
			if err := walk(a.ID, title+" / ", depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk("", "", 0)
}

// galleryFile: GET /company/photos/{id}/file (?dl=1 — to save) and …/thumb — the picture.
func (s *Server) galleryFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if p, _, _, ok := s.galleryPhoto(w, r, sess); ok {
		s.servePhoto(w, r, p)
	}
}

// servePhoto sends the picture, its thumbnail for a path in «/thumb».
func (s *Server) servePhoto(w http.ResponseWriter, r *http.Request, p company.Photo) {
	key, mime := p.FileKey, p.Mime
	if strings.HasSuffix(r.URL.Path, "/thumb") && p.ThumbKey != "" {
		key, mime = p.ThumbKey, "image/jpeg"
	}
	obj, err := s.Storages.Open(r.Context(), nil, key)
	if err != nil {
		s.fail(w, "open photo", err)
		return
	}
	defer obj.Close()
	disp := "inline"
	if r.URL.Query().Get("dl") == "1" {
		disp = "attachment"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(p.FileName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = io.Copy(w, obj)
}

// galleryPhotoSave: POST /company/photos/{id} — caption, or cover=1 (the album's cover), or album (moves it).
func (s *Server) galleryPhotoSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, a, keeper, ok := s.galleryPhoto(w, r, sess)
	if !ok {
		return
	}
	back := photoBase + "/" + p.ID
	var err error
	switch {
	case r.FormValue("cover") == "1":
		if a.CanChange(sess.UserID, keeper) {
			err = s.Company.SetCover(r.Context(), p.ID)
		} else {
			err = company.ErrForbidden
		}
	case !a.CanChange(sess.UserID, keeper) && p.CreatedBy != sess.UserID:
		err = company.ErrForbidden
	case r.FormValue("album") != "":
		to := r.FormValue("album")
		var target company.Album
		var sees bool
		if sees, err = s.Company.SeesAlbum(r.Context(), sess.UserID, to, keeper); err == nil {
			if target, err = s.Company.Album(r.Context(), to); err == nil && (!sees || !target.CanAdd(sess.UserID, keeper)) {
				err = company.ErrForbidden
			}
		}
		if err == nil {
			err = s.Company.MovePhoto(r.Context(), p.ID, to)
		}
	default:
		err = s.Company.SetCaption(r.Context(), p.ID, r.FormValue("caption"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// galleryPhotoDelete: POST /company/photos/{id}/delete — who changes the album, or who added the photo.
func (s *Server) galleryPhotoDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, a, keeper, ok := s.galleryPhoto(w, r, sess)
	if !ok {
		return
	}
	var err error = company.ErrForbidden
	if a.CanChange(sess.UserID, keeper) || p.CreatedBy == sess.UserID {
		err = s.Company.DeletePhoto(r.Context(), p.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, photoBase+"/"+p.ID)
		return
	}
	s.Log.Info("photo deleted", "user_id", sess.UserID, "id", p.ID)
	http.Redirect(w, r, galleryBase+"/"+a.ID, http.StatusSeeOther)
}

// galleryLike: POST /company/photos/{id}/like — «Нравится» on or off.
func (s *Server) galleryLike(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, _, _, ok := s.galleryPhoto(w, r, sess)
	if !ok {
		return
	}
	if _, _, err := s.Company.Like(r.Context(), p.ID, sess.UserID); err != nil {
		s.taskErr(w, r, err, photoBase+"/"+p.ID)
		return
	}
	http.Redirect(w, r, photoBase+"/"+p.ID, http.StatusSeeOther)
}

// galleryComment: POST /company/photos/{id}/comment (body).
func (s *Server) galleryComment(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, _, _, ok := s.galleryPhoto(w, r, sess)
	if !ok {
		return
	}
	back := photoBase + "/" + p.ID
	if _, err := s.Company.AddPhotoComment(r.Context(), p.ID, sess.UserID, r.FormValue("body")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#comments", http.StatusSeeOther)
}

// galleryCommentDelete: POST /company/photo-comments/{id}/delete — its author or a keeper.
func (s *Server) galleryCommentDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, photo, err := s.Company.PhotoComment(r.Context(), r.PathValue("id"))
	back := photoBase + "/" + photo
	if err == nil && c.UserID != sess.UserID {
		var keeper bool
		if keeper, err = s.galleryKeeper(r.Context(), sess); err == nil && !keeper {
			err = company.ErrForbidden
		}
	}
	if err == nil {
		err = s.Company.DeletePhotoComment(r.Context(), c.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#comments", http.StatusSeeOther)
}
