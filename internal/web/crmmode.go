package web

import (
	"net/http"
	"net/url"

	"go24/internal/auth"
	"go24/internal/i18n"
)

// crmMode: «Режим работы CRM» (administrators) — classic with leads, or simple without them (crm/mode.go); going
// simple with «convert» converts the leads in work.
func (s *Server) crmMode(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/deals"
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	done, failed, err := s.CRM.SetSimple(r.Context(), sess.UserID, r.FormValue("mode") == "simple", r.FormValue("convert") == "1")
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	msg := s.tr(r, "crm.saved")
	if done > 0 || failed > 0 {
		msg = s.tr(r, "crm.mode_converted", done, failed)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func init() {
	portalMethods["crm.settings.mode.get"] = portalMethod{Scope: "crm:read", Run: (*Server).portalCRMMode}
}

// portalCRMMode: crm.settings.mode.get — 1 classic, 2 simple.
func (s *Server) portalCRMMode(c portalCall) (portalOut, error) {
	simple, err := s.CRM.Simple(c.r.Context())
	if simple {
		return portalOut{Result: 2}, err
	}
	return portalOut{Result: 1}, err
}
