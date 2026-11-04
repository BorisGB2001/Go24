package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/theme"
)

// themeLook is the theme of the page as the layout draws it: data-theme, data-bg and the picture of one's own.
type themeLook struct {
	theme.Theme
	Own      bool   // the employee picked a theme of their own
	ImageURL string // the picture of one's own (Bg = custom), with its version
}

// pageTheme sets the theme of the page for the employee (theirs, the company's or go24's).
func (s *Server) pageTheme(ctx context.Context, p *page) {
	if s.Themes == nil || p.User == nil {
		return
	}
	t, own, err := s.Themes.Get(ctx, p.User.UserID)
	if err != nil {
		s.Log.Warn("theme", "user_id", p.User.UserID, "err", err)
		return
	}
	p.Look, p.ThemeBgs = themeLook{Theme: t, Own: own}, theme.Backgrounds
	if t.Bg == theme.Custom {
		sum := sha256.Sum256([]byte(t.Image))
		p.Look.ImageURL = "/appearance/bg?v=" + hex.EncodeToString(sum[:6])
	}
}

// appearanceSave keeps the theme as the «Тема оформления» form sends it: mode, bg, a picture of one's own (image)
// for bg=custom — a new one or the one kept before; all=1 — an administrator's «Применить для всех».
func (s *Server) appearanceSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, theme.MaxImage+64<<10)
	if err := r.ParseMultipartForm(theme.MaxImage + 64<<10); err != nil && err != http.ErrNotMultipart {
		s.taskErr(w, r, i18n.E("err.theme_image"), "/")
		return
	}
	back := themeBack(r)
	all := r.FormValue("all") == "1"
	if all && !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	t := theme.Theme{Mode: r.FormValue("mode"), Bg: r.FormValue("bg")}
	if t.Bg == theme.Custom {
		if f, hdr, err := r.FormFile("image"); err == nil {
			defer f.Close()
			if hdr.Size > theme.MaxImage {
				s.taskErr(w, r, i18n.E("err.theme_image"), back)
				return
			}
			owner := sess.UserID
			if all {
				owner = "company"
			}
			if t.Image, t.ImageType, err = s.storeThemeImage(r.Context(), sess.UserID, owner, f, hdr.Size); err != nil {
				s.taskErr(w, r, err, back)
				return
			}
		} else {
			// the picture picked before: the employee's own or the company's, whichever is in force
			cur, _, err := s.Themes.Get(r.Context(), sess.UserID)
			if all {
				cur, _, err = s.Themes.Default(r.Context())
			}
			if err != nil {
				s.taskErr(w, r, err, back)
				return
			}
			t.Image, t.ImageType = cur.Image, cur.ImageType
		}
	}
	t, err := theme.Check(t)
	if err == nil {
		if all {
			err = s.Themes.SaveDefault(r.Context(), t, sess.UserID)
		} else {
			err = s.Themes.Save(r.Context(), sess.UserID, t)
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("theme saved", "user_id", sess.UserID, "mode", t.Mode, "bg", t.Bg, "all", all)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// appearanceReset: «Сбросить» — the company's (or go24's) theme is back; all=1 — an administrator drops the
// company's theme.
func (s *Server) appearanceReset(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := themeBack(r)
	var err error
	if r.FormValue("all") == "1" {
		if !sess.Admin {
			s.taskErr(w, r, i18n.E("err.forbidden"), back)
			return
		}
		err = s.Themes.ResetDefault(r.Context())
	} else {
		err = s.Themes.Reset(r.Context(), sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// storeThemeImage keeps a picture for a background: JPEG, PNG, WebP or GIF, its type by its first bytes.
func (s *Server) storeThemeImage(ctx context.Context, userID, owner string, f io.Reader, size int64) (key, ct string, err error) {
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ct = http.DetectContentType(head[:n])
	ext, ok := photoTypes[ct]
	if !ok {
		return "", "", i18n.E("err.theme_image")
	}
	key = "go24/themes/" + owner + "/" + randomHex(8) + ext
	if err := s.Storages.Put(ctx, userID, false, "", key, io.MultiReader(bytes.NewReader(head[:n]), f), size, ct); err != nil {
		return "", "", err
	}
	return key, ct, nil
}

// appearanceBg serves the picture of the employee's background (their own or the company's).
func (s *Server) appearanceBg(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Themes == nil {
		http.NotFound(w, r)
		return
	}
	t, _, err := s.Themes.Get(r.Context(), sess.UserID)
	if r.URL.Query().Get("all") == "1" {
		t, _, err = s.Themes.Default(r.Context())
	}
	if err != nil || t.Bg != theme.Custom || t.Image == "" {
		http.NotFound(w, r)
		return
	}
	obj, err := s.Storages.Open(r.Context(), nil, t.Image)
	if err != nil {
		s.fail(w, "open theme image", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", t.ImageType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400") // the URL changes with every new picture
	_, _ = io.Copy(w, obj)
}

// apiTheme: GET gives the employee's theme ({mode, bg, own}); PUT sets their own ({mode, bg}; a picture of one's own
// is uploaded in the web form only — bg "custom" keeps the one there is). ?all=1 — the company's theme
// (administrators).
func (s *Server) apiTheme(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	all := r.URL.Query().Get("all") == "1"
	if all && !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	get := func() (theme.Theme, bool, error) {
		if all {
			t, set, err := s.Themes.Default(r.Context())
			return t, set, err
		}
		return s.Themes.Get(r.Context(), p.UserID)
	}
	t, own, err := get()
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodPut:
		var in struct{ Mode, Bg string }
		if err := readJSON(r, &in); err != nil {
			return err
		}
		next := theme.Theme{Mode: in.Mode, Bg: in.Bg}
		if next.Bg == theme.Custom {
			next.Image, next.ImageType = t.Image, t.ImageType
		}
		if t, err = theme.Check(next); err != nil {
			return err
		}
		if all {
			err = s.Themes.SaveDefault(r.Context(), t, p.UserID)
		} else {
			err = s.Themes.Save(r.Context(), p.UserID, t)
		}
		if err != nil {
			return err
		}
		own = true
	case http.MethodDelete:
		if all {
			err = s.Themes.ResetDefault(r.Context())
		} else {
			err = s.Themes.Reset(r.Context(), p.UserID)
		}
		if err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": t.Mode, "bg": t.Bg, "own": own})
	return nil
}

// themeBack is the page the «Тема оформления» form came from (the start page otherwise).
func themeBack(r *http.Request) string {
	if b := r.FormValue("back"); localPath(b) {
		return b
	}
	return "/"
}
