package web

import (
	"net/http"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
)

// ---------- «Контакт-центр» of the CRM (/crm/contact-center): every channel clients come through ----------

func (s *Server) crmContactCenter(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Kind": "contact-center", "Days": crm.RecentDays}
	if err := s.crmTabs(r, sess, data); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	list, err := s.CRM.ContactCenter(r.Context(), time.Now())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	data["Channels"] = list
	s.render(w, r, "crmcc", page{TitleKey: "crm.cc.title", Nav: "crm_deals", Data: data})
}

// apiCRMContactCenter: GET /api/v1/crm/contact-center — the channels, connected or not, with their counts.
func (s *Server) apiCRMContactCenter(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.CRM.ContactCenter(r.Context(), time.Now())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}
