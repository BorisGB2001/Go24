package web

import (
	"net/http"

	"go24/internal/crm"
	"go24/internal/tasks"
)

// «Видимость поля» of the CRM's own fields (GO24DEV-992): the pages show, take and export only the fields the viewer
// sees; a hidden one keeps its value when the card is saved.

// crmSight is the viewer's sight of the own fields; without a session — only the fields shown to everyone.
func (s *Server) crmSight(r *http.Request) (crm.FieldSight, error) {
	sess, err := s.session(r)
	if err != nil {
		return crm.FieldSight{}, nil
	}
	return s.CRM.FieldSightOf(r.Context(), sess.UserID, sess.Admin)
}

// portalSight is the REST caller's sight of the own fields.
func (s *Server) portalSight(c portalCall) (crm.FieldSight, error) {
	return s.CRM.FieldSightOf(c.r.Context(), c.p.UserID, s.apiAdmin(c.r.Context(), c.p.UserID))
}

// crmFieldsOf are the fields of the set the viewer sees.
func (s *Server) crmFieldsOf(r *http.Request, set tasks.FieldSet) ([]tasks.Field, error) {
	list, err := s.Tasks.FieldsOf(r.Context(), set)
	if err != nil {
		return nil, err
	}
	v, err := s.crmSight(r)
	return v.Fields(list), err
}

// crmFieldValuesOf are the card's fields the viewer sees, with their values.
func (s *Server) crmFieldValuesOf(r *http.Request, set tasks.FieldSet, id string) ([]tasks.FieldValue, error) {
	list, err := s.Tasks.FieldValuesOf(r.Context(), set, id)
	if err != nil {
		return nil, err
	}
	v, err := s.crmSight(r)
	return v.Values(list), err
}
