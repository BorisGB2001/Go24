package web

import (
	"net/http"
	"net/url"
	"strings"
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
	err := s.crmTabs(r, sess, data)
	rel := map[string][]string{} // «Связи»: what the elements of each are bound to
	types, _ := data["Types"].([]crm.Type)
	for i := 0; i < len(types) && err == nil; i++ {
		rel[types[i].ID], err = s.CRM.TypeParents(r.Context(), types[i].ID)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	data["Relations"] = rel
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
		if err == nil && r.FormValue("with_relations") == "1" { // «Связи»: "deals" stands for the deals
			var parents []string
			for _, p := range r.Form["parents"] {
				parents = append(parents, strings.TrimPrefix(p, "deals"))
			}
			err = s.CRM.SetTypeParents(r.Context(), id, parents)
		}
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

// crmParentSlot is a kind of cards an element is bound to: its parent of that kind and the cards to choose from.
type crmParentSlot struct {
	Kind, Name string // Name "" — the deals
	Parent     *crm.Deal
	Options    []crm.Deal
}

// crmRelations fills the «Связи» of a deal or an element: the parents of each kind its smart process is bound to,
// and the child elements of each smart process bound to its kind.
func (s *Server) crmRelations(r *http.Request, sess auth.Session, d crm.Deal, data map[string]any) error {
	if d.TypeID != "" {
		kinds, err := s.CRM.TypeParents(r.Context(), d.TypeID)
		if err != nil {
			return err
		}
		parents, err := s.CRM.Parents(r.Context(), d.ID)
		if err != nil {
			return err
		}
		var slots []crmParentSlot
		for _, k := range kinds {
			slot := crmParentSlot{Kind: k}
			if k != "" {
				t, err := s.CRM.Type(r.Context(), k)
				if err != nil {
					return err
				}
				slot.Name = t.Name
			}
			for i := range parents {
				if parents[i].TypeID == k {
					slot.Parent = &parents[i]
				}
			}
			if slot.Options, err = s.CRM.DealsOfKind(r.Context(), sess.UserID, k, ""); err != nil {
				return err
			}
			slots = append(slots, slot)
		}
		data["ParentSlots"] = slots
	}
	types, err := s.CRM.ChildTypes(r.Context(), d.TypeID)
	if err != nil || len(types) == 0 {
		return err
	}
	children, err := s.CRM.Children(r.Context(), sess.UserID, d.ID)
	if err != nil {
		return err
	}
	var groups []crmChildGroup
	for _, t := range types {
		g := crmChildGroup{crmSmart: crmSmart{Type: t}}
		for _, c := range children {
			if c.TypeID == t.ID {
				g.Items = append(g.Items, c)
			}
		}
		pipes, err := s.CRM.PipelinesOf(r.Context(), t.ID)
		if err != nil {
			return err
		}
		if len(pipes) > 0 {
			g.Pipeline = pipes[0].ID
		}
		groups = append(groups, g)
	}
	data["ChildGroups"] = groups
	return nil
}

// crmChildGroup are the child elements of a smart process in their parent's card; new ones go to Pipeline.
type crmChildGroup struct {
	crmSmart
	Pipeline string
}

// crmParent binds an element to its parent of a kind, or unbinds it (parent "").
func (s *Server) crmParent(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	err := s.crmCheck(r, sess, "deals", id, "write")
	if p := r.FormValue("parent"); err == nil && p != "" {
		err = s.crmCheck(r, sess, "deals", p, "read")
	}
	if err == nil {
		err = s.CRM.SetParent(r.Context(), sess.UserID, id, strings.TrimPrefix(r.FormValue("kind"), "deals"), r.FormValue("parent"))
	}
	if err != nil {
		s.crmErr(w, r, err, "/crm/deals/"+id)
		return
	}
	http.Redirect(w, r, "/crm/deals/"+id+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}
