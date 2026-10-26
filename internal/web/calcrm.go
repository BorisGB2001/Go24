package web

import (
	"net/http"
	"slices"
	"strings"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «CRM» of a meeting: the form links deals, leads, contacts and companies, the meeting's page shows
// them, a card shows its meetings and plans one («Запланировать встречу», /calendar/new?crm=deal:ID).

// crmRefCard is the card of ref the person may see, with its title.
func (s *Server) crmRefCard(r *http.Request, sess auth.Session, ref calendar.CRMRef) (calendar.CRMRef, error) {
	kind := crmPaths[ref.Entity]
	if kind == "" {
		return ref, i18n.E("err.bad_form")
	}
	if err := s.crmCheck(r, sess, kind, ref.ID, "read"); err != nil {
		return ref, err
	}
	var err error
	switch ref.Entity {
	case "deal":
		var d crm.Deal
		d, err = s.CRM.Deal(r.Context(), ref.ID)
		ref.Title = d.Title
	case "lead":
		var l crm.Lead
		l, err = s.CRM.Lead(r.Context(), ref.ID)
		ref.Title = l.Title
	case "contact":
		var c crm.Contact
		c, err = s.CRM.Contact(r.Context(), ref.ID)
		ref.Title = c.Name
	case "company":
		var c crm.Company
		c, err = s.CRM.Company(r.Context(), ref.ID)
		ref.Title = c.Title
	}
	return ref, err
}

// parseCRMRef reads kind:id of the form.
func parseCRMRef(v string) (calendar.CRMRef, bool) {
	kind, id, _ := strings.Cut(v, ":")
	ref := calendar.CRMRef{Entity: kind, ID: id}
	return ref, ref.Valid()
}

// eventCRMData: the meeting's cards for its page and form (CRMOn — the form has the field), a new meeting from a
// card (?crm=) linked to it and going back there, a copy linked to the cards of the copied one.
func (s *Server) eventCRMData(r *http.Request, sess auth.Session, e calendar.Event, copied string, data map[string]any) error {
	if s.CRM == nil || tasks.IsGuestCtx(r.Context()) || e.Hidden {
		return nil
	}
	data["CRMOn"] = data["CanEdit"] == true
	var err error
	switch {
	case e.ID != "":
		data["CRM"], err = s.Calendar.CRM(r.Context(), e.ID)
	case copied != "":
		data["CRM"], err = s.Calendar.CRM(r.Context(), copied)
	default:
		if ref, ok := parseCRMRef(r.URL.Query().Get("crm")); ok {
			if ref, err = s.crmRefCard(r, sess, ref); err == nil {
				data["CRM"], data["CRMBack"] = []calendar.CRMRef{ref}, ref.Path()
			}
		}
	}
	return err
}

// eventCRMForm: the cards the meeting form links; a card linked anew is one the person may see.
func (s *Server) eventCRMForm(r *http.Request, sess auth.Session, eventID string) ([]calendar.CRMRef, error) {
	var had []calendar.CRMRef
	if eventID != "" {
		var err error
		if had, err = s.Calendar.CRM(r.Context(), eventID); err != nil {
			return nil, err
		}
	}
	var refs []calendar.CRMRef
	for _, v := range r.Form["crm"] {
		ref, ok := parseCRMRef(v)
		if !ok {
			return nil, i18n.E("err.bad_form")
		}
		if !slices.ContainsFunc(had, func(h calendar.CRMRef) bool { return h.Entity == ref.Entity && h.ID == ref.ID }) {
			if _, err := s.crmRefCard(r, sess, ref); err != nil {
				return nil, err
			}
		}
		refs = append(refs, ref)
	}
	return refs, nil
}
