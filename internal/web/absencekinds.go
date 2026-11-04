package web

import (
	"context"
	"net/http"
	"strconv"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/tasks"
)

// «Типы отсутствий» of the absence chart: an administrator or an HR officer with the right «absences»
// renames, recolours and turns off the built-in reasons and adds the company's own.

// canAbsenceKinds: an administrator or an HR officer with «absences».
func (s *Server) canAbsenceKinds(ctx context.Context, userID string, admin bool) (bool, error) {
	if admin {
		return true, nil
	}
	hr, err := s.Company.HROf(ctx, userID)
	return hr.Can("absences"), err
}

// absenceKeys: the keys of the reasons offered for new absences (the built-in ones if they cannot be read).
func (s *Server) absenceKeys(ctx context.Context) []string {
	kinds, err := s.Company.ActiveAbsenceKinds(ctx)
	if err != nil {
		s.Log.Warn("absence kinds", "err", err)
		return company.AbsenceKinds
	}
	keys := make([]string, len(kinds))
	for i, k := range kinds {
		keys[i] = k.Key
	}
	return keys
}

// absenceKindsPage: GET /company/absences/kinds.
func (s *Server) absenceKindsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if ok, err := s.canAbsenceKinds(r.Context(), sess.UserID, tasks.IsAdmin(r.Context())); err != nil || !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	list, err := s.Company.AbsenceKindList(r.Context())
	if err != nil {
		s.fail(w, "absence kinds", err)
		return
	}
	var flash string
	if r.URL.Query().Get("saved") == "1" {
		flash = s.tr(r, "portal.saved")
	}
	s.render(w, r, "absencekinds", page{TitleKey: "abskinds.title", Nav: "absences", Flash: flash, Data: map[string]any{
		"Kinds": list, "Colors": company.AbsenceColors}})
}

// absenceKindsSave: POST /company/absences/kinds — rows key.N, name.N, color.N, off.N (a built-in or own reason; the
// own ones without del.N stay), and a new one from new_name, new_color.
func (s *Server) absenceKindsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if ok, err := s.canAbsenceKinds(r.Context(), sess.UserID, tasks.IsAdmin(r.Context())); err != nil || !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	var in []company.AbsenceKind
	for i := 0; r.Form.Has("key." + strconv.Itoa(i)); i++ {
		n := strconv.Itoa(i)
		if r.FormValue("del."+n) != "" {
			continue
		}
		in = append(in, company.AbsenceKind{Key: r.FormValue("key." + n), Name: r.FormValue("name." + n),
			Color: r.FormValue("color." + n), Off: r.FormValue("off."+n) != ""})
	}
	if name := r.FormValue("new_name"); name != "" {
		in = append(in, company.AbsenceKind{Name: name, Color: r.FormValue("new_color")})
	}
	if err := s.Company.SaveAbsenceKinds(r.Context(), in); err != nil {
		s.taskErr(w, r, err, "/company/absences/kinds")
		return
	}
	s.audit(r, audit.Event{Kind: "absence_kinds_saved", UserID: sess.UserID})
	http.Redirect(w, r, "/company/absences/kinds?saved=1", http.StatusSeeOther)
}

type apiAbsenceKind struct {
	company.AbsenceKind
	Title string `json:"title"` // the name as shown: own or the built-in one in the request's language
}

// GET /api/v1/absences/kinds — the reasons of absence (?all=1 — with the ones turned off).
func (s *Server) apiAbsenceKinds(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Company.AbsenceKindList(r.Context())
	if err != nil {
		return err
	}
	out := []apiAbsenceKind{}
	for _, k := range list {
		if !k.Off || r.URL.Query().Get("all") == "1" {
			out = append(out, apiAbsenceKind{k, company.AbsenceKindName(k.Key, func(key string, args ...any) string { return s.tr(r, key, args...) })})
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// PUT /api/v1/absences/kinds [{"key","name","color","off"}…] — as the page: the own reasons left out are deleted (turned
// off when absences have them), one without a key is new; an administrator or an HR officer with «absences».
func (s *Server) apiAbsenceKindsSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if ok, err := s.canAbsenceKinds(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID)); err != nil {
		return err
	} else if !ok {
		return company.ErrForbidden
	}
	var in []company.AbsenceKind
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Company.SaveAbsenceKinds(r.Context(), in); err != nil {
		return err
	}
	s.Log.Info("api absence kinds saved", "user_id", p.UserID)
	return s.apiAbsenceKinds(w, r, p)
}
