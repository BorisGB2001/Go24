package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Настроить карточку» of the CRM: the card's fields by sections, the hidden ones, the common view ----------

// crmCardLayout puts the card's view into its data: Layout (the sections), Hidden, FieldBy (the own fields by
// "f:ID"), HiddenOpen (a hidden field must be filled — the form opens them); ?layout=1 — the view's editor.
func (s *Server) crmCardLayout(r *http.Request, sess auth.Session, data map[string]any, typeID string) error {
	entity := data["Entity"].(string)
	scope := crm.CardScope(entity, typeID)
	fields, _ := data["Fields"].([]tasks.FieldValue)
	ids, by := []string{}, map[string]tasks.FieldValue{}
	for _, f := range fields {
		ids = append(ids, f.ID)
		by["f:"+f.ID] = f
	}
	l, own, common, err := s.CRM.CardLayout(r.Context(), sess.UserID, scope, ids)
	if err != nil {
		return err
	}
	stage := ""
	switch it := data["Item"].(type) {
	case crm.Deal:
		stage = it.Stage.ID
	case crm.Lead:
		stage = it.Stage.ID
	}
	open := slices.Contains(l.Hidden, "responsible")
	for _, c := range l.Hidden {
		if f, ok := by[c]; ok && f.RequiredAt(stage) {
			open = true
		}
	}
	data["Layout"], data["Hidden"], data["HiddenOpen"], data["FieldBy"] = l.Sections, l.Hidden, open, by
	data["LayoutKind"], data["LayoutOwn"], data["LayoutCommon"] = crm.ColumnKind(scope), own, common
	data["LayoutEdit"], data["LayoutAdmin"] = r.URL.Query().Get("layout") == "1", sess.Admin
	return nil
}

// crmSaveLayout saves the view of the card's kind: the person's own, or (an administrator) the common one; reset —
// «Вернуть общий вид» (mine) or «Сбросить на стандартный» (common).
func (s *Server) crmSaveLayout(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	entity := crmEntities[kind]
	err := s.crmCheck(r, sess, kind, id, "read")
	typeID := ""
	if err == nil && entity == "deal" {
		var d crm.Deal
		if d, err = s.CRM.Deal(r.Context(), id); err == nil {
			typeID = d.TypeID
		}
	}
	scope := crm.CardScope(entity, typeID)
	common := r.FormValue("common") == "1" || r.FormValue("reset") == "common"
	if err == nil && common && !sess.Admin {
		err = i18n.E("err.forbidden")
	}
	var ids []string
	if err == nil {
		var fields []tasks.FieldValue
		if fields, err = s.Tasks.FieldValuesOf(r.Context(), tasks.CRMFieldSets[entity], id); err == nil {
			for _, f := range fields {
				ids = append(ids, f.ID)
			}
		}
	}
	msg := "crm.card.saved"
	if err == nil {
		switch reset := r.FormValue("reset"); {
		case reset != "":
			err, msg = s.CRM.ResetCardLayout(r.Context(), sess.UserID, scope, common), "crm.card.reset_done"
		default:
			var l crm.CardLayout
			if json.NewDecoder(strings.NewReader(r.FormValue("layout"))).Decode(&l) != nil {
				err = i18n.E("err.bad_form")
			} else {
				err = s.CRM.SetCardLayout(r.Context(), sess.UserID, scope, l, ids, common)
			}
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back+"?layout=1")
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, msg)), http.StatusSeeOther)
}
