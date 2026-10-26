package web

import (
	"net/http"
	"net/url"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Смарт-процессы»: /crm/types (administrators set them up), each one's kanban at /crm/deals?type=ID ----------

// crmTabs fills what the CRM's tabs show: the counter of deeds and the smart processes.
func (s *Server) crmTabs(r *http.Request, sess auth.Session, data map[string]any) error {
	var err error
	if data["ActCount"], err = s.CRM.ActivityCounter(r.Context(), sess.UserID, time.Now()); err != nil {
		return err
	}
	if data["Types"], err = s.CRM.Types(r.Context()); err != nil {
		return err
	}
	// «Валюты»: the list for the forms; the sums of the lists are in the base one
	list, err := s.CRM.Currencies(r.Context())
	for _, c := range list {
		if c.Base {
			data["Base"] = c.Code
		}
	}
	data["Currencies"] = list
	return err
}

func (s *Server) crmTypes(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Kind": "types", "Colors": tasks.StageColors}
	if err := s.crmTabs(r, sess, data); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmtypes", page{TitleKey: "crm.types", Nav: "crm_types", Data: data})
}

// crmTypeSave: the administrator adds, changes or deletes a smart process.
func (s *Server) crmTypeSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/types?"
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/crm/types")
		return
	}
	var err error
	id := r.FormValue("id")
	switch r.FormValue("op") {
	case "add":
		if id, err = s.CRM.AddType(r.Context(), r.FormValue("name"), r.FormValue("color")); err == nil {
			back = "/crm/deals?type=" + url.QueryEscape(id) + "&"
		}
	case "update":
		err = s.CRM.UpdateType(r.Context(), id, r.FormValue("name"), r.FormValue("color"))
	case "delete":
		err = s.CRM.DeleteType(r.Context(), id)
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/types")
		return
	}
	http.Redirect(w, r, back+"msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// crmTypeOf is the smart process the list or the card is of: by its pipeline, else by "type" (nil — deals).
func (s *Server) crmTypeOf(r *http.Request, pipelineID, typeID string) (*crm.Type, error) {
	if pipelineID != "" {
		if t, err := s.CRM.PipelineType(r.Context(), pipelineID); err == nil {
			typeID = t
		}
	}
	if typeID == "" {
		return nil, nil
	}
	t, err := s.CRM.Type(r.Context(), typeID)
	return &t, err
}

// crmSmart are the cards of a smart process linked to a client.
type crmSmart struct {
	Type  crm.Type
	Items []crm.Deal
}

// crmSplitDeals parts the deals of a client from the cards of the smart processes (in their order).
func (s *Server) crmSplitDeals(r *http.Request, list []crm.Deal) ([]crm.Deal, []crmSmart, error) {
	var deals []crm.Deal
	byType := map[string][]crm.Deal{}
	for _, d := range list {
		if d.TypeID == "" {
			deals = append(deals, d)
		} else {
			byType[d.TypeID] = append(byType[d.TypeID], d)
		}
	}
	if len(byType) == 0 {
		return deals, nil, nil
	}
	types, err := s.CRM.Types(r.Context())
	var smart []crmSmart
	for _, t := range types {
		if items := byType[t.ID]; len(items) > 0 {
			smart = append(smart, crmSmart{Type: t, Items: items})
		}
	}
	return deals, smart, err
}
