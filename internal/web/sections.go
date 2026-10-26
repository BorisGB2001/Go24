package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
)

// portalSections are the sections an administrator switches off for the whole company, as «Настройки →
// Инструменты»: the menu item (menu.Builtin), its name, its pages (and REST /api/v1/<the same>). Tasks,
// projects, the company and the messenger stay: go24 is built around them.
var portalSections = []struct {
	ID, TitleKey string
	Paths        []string
}{
	{"feed", "nav.feed", []string{"/feed"}},
	{"calendar", "calendar.title", []string{"/calendar"}},
	{"files", "nav.sec.files", []string{"/disk"}},
	{"collabs", "collab.title", []string{"/collabs"}},
	{"crm", "crm.title", []string{"/crm"}},
	{"marketing", "mkt.title", []string{"/marketing"}},
	{"store", "stock.title", []string{"/store"}},
	{"mail", "mail.title", []string{"/mail"}},
	{"openlines", "ol.title", []string{"/openlines"}},
	{"bp", "bp.title", []string{"/bp"}},
	{"kb", "kb.title", []string{"/kb"}},
	{"sites", "site.title", []string{"/sites"}},
	{"booking", "booking.title", []string{"/booking"}},
	{"bi", "bi.title", []string{"/bi"}},
	{"sign", "sign.title", []string{"/sign"}},
}

const sectionsKey = "sections_off" // company_settings: the ids switched off

// sectionsOff are the sections switched off; read again at most every 15 seconds (at once after a change here).
func (s *Server) sectionsOff(ctx context.Context) []string {
	if s.DB == nil {
		return nil
	}
	s.secMu.Lock()
	defer s.secMu.Unlock()
	if time.Since(s.secAt) < 15*time.Second {
		return s.secOff
	}
	var raw []byte
	var off []string
	err := s.DB.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, sectionsKey).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &off)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.Log.Warn("sections off", "err", err)
		return s.secOff
	}
	s.secOff, s.secAt = off, time.Now()
	return off
}

// saveSectionsOff keeps the sections switched off (unknown ids are dropped) and writes it in «Журнал событий».
func (s *Server) saveSectionsOff(r *http.Request, by string, ids []string) error {
	var off []string
	for _, sec := range portalSections {
		if slices.Contains(ids, sec.ID) {
			off = append(off, sec.ID)
		}
	}
	raw, err := json.Marshal(off)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		sectionsKey, raw, by); err != nil {
		return err
	}
	s.secMu.Lock()
	s.secAt = time.Time{}
	s.secMu.Unlock()
	s.audit(r, audit.Event{Kind: "sections_saved", UserID: by, Target: strings.Join(off, ", ")})
	return nil
}

// offSection is the section switched off that the path belongs to (its page or its REST), or nil.
func (s *Server) offSection(ctx context.Context, path string) (titleKey string, ok bool) {
	if !strings.HasPrefix(path, "/") || path == "/" {
		return "", false
	}
	off := s.sectionsOff(ctx)
	if len(off) == 0 {
		return "", false
	}
	page := strings.TrimPrefix(path, "/api/v1")
	for _, sec := range portalSections {
		if !slices.Contains(off, sec.ID) {
			continue
		}
		for _, p := range sec.Paths {
			if page == p || strings.HasPrefix(page, p+"/") {
				return sec.TitleKey, true
			}
		}
	}
	return "", false
}

// sectionsGate answers the pages and REST of a section switched off: «Раздел отключён» (404); the data stay.
func (s *Server) sectionsGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, off := s.offSection(r.Context(), r.URL.Path)
		if !off {
			next.ServeHTTP(w, r)
			return
		}
		msg := s.tr(r, "sections.off", s.tr(r, key))
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": msg})
			return
		}
		http.Error(w, msg, http.StatusNotFound)
	})
}

// availOn drops from the menu's items the sections switched off.
func (s *Server) availOn(ctx context.Context, avail []string) []string {
	off := s.sectionsOff(ctx)
	return slices.DeleteFunc(avail, func(id string) bool { return slices.Contains(off, id) })
}

// sectionsPage is «Инструменты» of «Настройки» (administrators): the sections on and off.
func (s *Server) sectionsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.taskErr(w, r, i18n.E("err.bad_form"), "/settings/tools")
			return
		}
		var off []string
		for _, sec := range portalSections {
			if !slices.Contains(r.Form["on"], sec.ID) {
				off = append(off, sec.ID)
			}
		}
		if err := s.saveSectionsOff(r, sess.UserID, off); err != nil {
			s.taskErr(w, r, err, "/settings/tools")
			return
		}
		http.Redirect(w, r, "/settings/tools?msg="+url.QueryEscape(s.tr(r, "sections.saved")), http.StatusSeeOther)
		return
	}
	s.render(w, r, "sections", page{TitleKey: "sections.title", Nav: "settings", Data: map[string]any{
		"Sections": portalSections, "Off": s.sectionsOff(r.Context())}})
}

// apiSections: GET /api/v1/settings/sections — the sections and whether each is on; PUT {"off": [ids]} — those
// switched off (the rest on). Administrators.
func (s *Server) apiSections(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	if r.Method == http.MethodPut {
		var in struct {
			Off []string `json:"off"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.saveSectionsOff(r, p.UserID, in.Off); err != nil {
			return err
		}
	}
	off := s.sectionsOff(r.Context())
	type item struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		On    bool   `json:"on"`
	}
	out := []item{}
	for _, sec := range portalSections {
		out = append(out, item{sec.ID, s.tr(r, sec.TitleKey), !slices.Contains(off, sec.ID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sections": out})
	return nil
}
