package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Настроить вид карточки» of the CRM kanban: the fields of the tiles (crm.Tile).

// crmTileFixed are the tile's fields with their own places: the amount under the title, the deed and the responsible
// at the foot; the others are lines with their names between them.
var crmTileFixed = []string{"amount", "activities", "responsible"}

// crmScopeFields are the custom fields of the cards of a scope ("deal", "lead", "type:ID") and their ids.
func (s *Server) crmScopeFields(r *http.Request, scope string) (string, []tasks.Field, []string, error) {
	entity := scope
	if strings.HasPrefix(scope, "type:") {
		entity = "deal"
	}
	set, ok := tasks.CRMFieldSets[entity]
	if !ok {
		return entity, nil, nil, i18n.E("err.bad_form")
	}
	fields, err := s.crmFieldsOf(r, set)
	ids := make([]string, len(fields))
	for i, f := range fields {
		ids[i] = f.ID
	}
	return entity, fields, ids, err
}

// crmTileData puts the view of the kanban's tiles into data: TileCols for the gear (the shown ones in their order,
// then the others), TileOn, TileLines (the shown fields between the amount and the foot) and the custom fields'
// values and the observers when shown.
func (s *Server) crmTileData(r *http.Request, sess auth.Session, data map[string]any, scope string, ids []string) error {
	entity, fields, fids, err := s.crmScopeFields(r, scope)
	if err != nil {
		return err
	}
	on, own, common, err := s.CRM.Tile(r.Context(), sess.UserID, scope, fids)
	if err != nil {
		return err
	}
	byKey := map[string]crmCol{}
	all := slices.Clone(crm.TileFields[crm.ColumnKind(scope)])
	for _, c := range all {
		byKey[c] = crmCol{Key: c}
	}
	for i := range fields {
		k := "f:" + fields[i].ID
		byKey[k] = crmCol{Key: k, Field: &fields[i]}
		all = append(all, k)
	}
	shown := map[string]bool{}
	var cols, lines []crmCol
	for _, k := range on {
		c := byKey[k]
		c.On, shown[k] = true, true
		cols = append(cols, c)
		if !slices.Contains(crmTileFixed, k) {
			lines = append(lines, c)
		}
	}
	for _, k := range all {
		if !shown[k] {
			cols = append(cols, byKey[k])
		}
	}
	data["TileCols"], data["TileOn"], data["TileLines"], data["TileScope"] = cols, shown, lines, scope
	data["TileOwn"], data["TileCommon"] = own, common
	var custom bool
	for _, c := range lines {
		custom = custom || c.Field != nil
	}
	if custom {
		if data["Values"], err = s.crmFieldValues(r, entity, fields, ids); err != nil {
			return err
		}
	}
	if shown["observers"] {
		data["Observers"], err = s.CRM.ObserversOf(r.Context(), entity, ids)
	}
	return err
}

// crmFieldValues are the custom fields' values of the cards as people read them, by card and field.
func (s *Server) crmFieldValues(r *http.Request, entity string, fields []tasks.Field, ids []string) (map[string]map[string]string, error) {
	vals, err := s.Tasks.ValuesOf(r.Context(), tasks.CRMFieldSets[entity], ids)
	if err != nil {
		return nil, err
	}
	for _, f := range fields { // dates as people read them
		if f.Kind == "date" {
			for _, v := range vals {
				if x, ok := v[f.ID]; ok {
					v[f.ID] = tasks.FieldValue{Field: f, Value: x}.Text(s.tr(r, "format.date"))
				}
			}
		}
	}
	return vals, nil
}

// crmSetTile saves the view of the tiles: POST scope, cols in their order; common=1 — for everybody (administrators);
// reset=own — back to the common view, reset=common — the common one back to the standard.
func (s *Server) crmSetTile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/crm/deals")
		return
	}
	back := crmBack(r.FormValue("back"))
	scope, reset := r.FormValue("scope"), r.FormValue("reset")
	common := r.FormValue("common") == "1" || reset == "common"
	_, _, fids, err := s.crmScopeFields(r, scope)
	if err == nil && common && !sess.Admin {
		err = i18n.E("err.forbidden")
	}
	msg := "crm.tile.saved"
	if err == nil {
		if reset != "" {
			err, msg = s.CRM.ResetTile(r.Context(), sess.UserID, scope, common), "crm.tile.reset_done"
		} else {
			cols := r.Form["cols"]
			if cols == nil {
				cols = []string{} // all unticked: only the title
			}
			err = s.CRM.SetTile(r.Context(), sess.UserID, scope, cols, fids, common)
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(s.tr(r, msg)), http.StatusSeeOther)
}

// crmTileJSON is the view of the tiles in REST.
type crmTileJSON struct {
	Scope  string   `json:"scope"`
	Fields []string `json:"fields"`
	Own    bool     `json:"own"`
	Common bool     `json:"common"`
}

// GET /api/v1/crm/tile?scope=deal|lead|type:ID — the fields the key's owner sees on the kanban's tiles, in order.
func (s *Server) apiCRMTile(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	scope := r.URL.Query().Get("scope")
	_, _, fids, err := s.crmScopeFields(r, scope)
	if err != nil {
		return err
	}
	cols, own, common, err := s.CRM.Tile(r.Context(), p.UserID, scope, fids)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, crmTileJSON{Scope: scope, Fields: cols, Own: own, Common: common})
	return nil
}

// PUT /api/v1/crm/tile {"scope": "deal", "fields": ["amount", "client", "f:…"], "common": false} saves the view;
// "reset": true — the own view back to the common one (with "common" — the common one back to the standard).
func (s *Server) apiCRMTileSet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Scope  string   `json:"scope"`
		Fields []string `json:"fields"`
		Common bool     `json:"common"`
		Reset  bool     `json:"reset"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	_, _, fids, err := s.crmScopeFields(r, in.Scope)
	if err != nil {
		return err
	}
	if in.Common && !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	if in.Reset {
		err = s.CRM.ResetTile(r.Context(), p.UserID, in.Scope, in.Common)
	} else {
		if in.Fields == nil {
			in.Fields = []string{}
		}
		err = s.CRM.SetTile(r.Context(), p.UserID, in.Scope, in.Fields, fids, in.Common)
	}
	if err != nil {
		return err
	}
	q := url.Values{"scope": {in.Scope}}
	r.URL.RawQuery = q.Encode()
	return s.apiCRMTile(w, r, p)
}
