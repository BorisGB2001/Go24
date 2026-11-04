package web

import (
	"cmp"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"go24/internal/crm"
)

// REST compatible with the portal, the access rights of the document generator (GO24DEV-987): documentgenerator.role.*
// — roles with their permissions writes them ({"TEMPLATES": {"MODIFY": "X"}, "DOCUMENTS": {"VIEW": "X",
// "MODIFY": ""}, "SETTINGS": {"MODIFY": ""}}; "X", "A" and "D" — allowed, "" — not) and whom they are given to (access
// codes U<number>, DR<number>, D<number>). As, the roles are of those who change the settings.

func init() {
	for name, m := range map[string]portalMethod{
		"documentgenerator.role.list":         {Scope: "documents:read", Run: (*Server).portalDocRoleList},
		"documentgenerator.role.get":          {Scope: "documents:read", Run: (*Server).portalDocRoleGet},
		"documentgenerator.role.add":          {Scope: "documents:write", Run: (*Server).portalDocRoleAdd},
		"documentgenerator.role.update":       {Scope: "documents:write", Run: (*Server).portalDocRoleUpdate},
		"documentgenerator.role.delete":       {Scope: "documents:write", Run: (*Server).portalDocRoleDelete},
		"documentgenerator.role.fillaccesses": {Scope: "documents:write", Run: (*Server).portalDocRoleFill},
	} {
		portalMethods[name] = m
	}
}

// portalDocPerms are the entity and action of each right of go24.
var portalDocPerms = map[string][2]string{"templates": {"TEMPLATES", "MODIFY"}, "view": {"DOCUMENTS", "VIEW"},
	"modify": {"DOCUMENTS", "MODIFY"}, "settings": {"SETTINGS", "MODIFY"}}

// portalDocRole is a role gives it, with its access codes.
func (s *Server) portalDocRole(c portalCall, r crm.DocRole) map[string]any {
	perms := map[string]map[string]string{}
	for key, ea := range portalDocPerms {
		if perms[ea[0]] == nil {
			perms[ea[0]] = map[string]string{}
		}
		perms[ea[0]][ea[1]] = ""
		if r.Perms.Can(key) {
			perms[ea[0]][ea[1]] = "X"
		}
	}
	name := r.Name
	if r.Code != "" && name == "" {
		name = s.tr(c.r, "docroles.role."+r.Code)
	}
	codes := []string{}
	if len(r.Members) > 0 {
		codes = s.portalAccess(c.r.Context(), r.Members)
	}
	return map[string]any{"id": r.Num, "name": name, "code": strings.ToUpper(r.Code), "isDefault": r.Default,
		"permissions": perms, "accessCodes": codes}
}

// portalDocRoleOf is the role of «id» (its number or go24 id).
func (s *Server) portalDocRoleOf(c portalCall, v any) (crm.DocRole, error) {
	roles, err := s.CRM.DocRoles(c.r.Context())
	if err != nil {
		return crm.DocRole{}, err
	}
	x := strings.TrimSpace(portalStr(v))
	for _, r := range roles {
		if x != "" && (x == r.ID || x == strconv.FormatInt(r.Num, 10)) {
			return r, nil
		}
	}
	return crm.DocRole{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
}

// documentgenerator.role.list: start → {"roles": […]}.
func (s *Server) portalDocRoleList(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "settings"); err != nil {
		return portalOut{}, err
	}
	roles, err := s.CRM.DocRoles(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	slices.SortFunc(roles, func(a, b crm.DocRole) int { return cmp.Compare(a.Num, b.Num) })
	rows := make([]map[string]any, 0, len(roles))
	for _, r := range roles {
		rows = append(rows, s.portalDocRole(c, r))
	}
	return portalListPage(c, "roles", rows), nil
}

// documentgenerator.role.get: id → {"role": …}.
func (s *Server) portalDocRoleGet(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "settings"); err != nil {
		return portalOut{}, err
	}
	r, err := s.portalDocRoleOf(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"role": s.portalDocRole(c, r)}}, nil
}

// portalDocRoleSave writes fields (name, permissions) over the role and answers it.
func (s *Server) portalDocRoleSave(c portalCall, r crm.DocRole) (portalOut, error) {
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	if v, ok := portalParamOK(fields, "name"); ok {
		r.Name = portalStr(v)
	}
	if r.Perms == nil {
		r.Perms = crm.DocPerms{}
	}
	if perms := portalMap(portalParam(fields, "permissions")); perms != nil {
		for key, ea := range portalDocPerms {
			if v, ok := portalParamOK(portalMap(portalParam(perms, ea[0])), ea[1]); ok {
				r.Perms[key] = "none"
				if x := strings.ToUpper(strings.TrimSpace(portalStr(v))); x == "X" || x == "A" || x == "D" {
					r.Perms[key] = "all"
				}
			}
		}
	}
	id, err := s.CRM.SaveDocRole(c.r.Context(), r, nil)
	if err != nil {
		return portalOut{}, err
	}
	s.Log.Info("docgen role saved", "user_id", c.p.UserID, "role", id, "api", true)
	if r, err = s.portalDocRoleOf(c, id); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"role": s.portalDocRole(c, r)}}, nil
}

// documentgenerator.role.add: fields[name], fields[permissions] → {"role": …}; given to nobody.
func (s *Server) portalDocRoleAdd(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "settings"); err != nil {
		return portalOut{}, err
	}
	return s.portalDocRoleSave(c, crm.DocRole{})
}

// documentgenerator.role.update: id, fields[name], fields[permissions] → {"role": …}; the permissions not given stay.
func (s *Server) portalDocRoleUpdate(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "settings"); err != nil {
		return portalOut{}, err
	}
	r, err := s.portalDocRoleOf(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if r.Code != "" {
		r.Name = "" // a built-in role keeps its name from the locales unless one is given
	}
	return s.portalDocRoleSave(c, r)
}

// documentgenerator.role.delete: id → null; the default role stays.
func (s *Server) portalDocRoleDelete(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "settings"); err != nil {
		return portalOut{}, err
	}
	r, err := s.portalDocRoleOf(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{}, s.CRM.DeleteDocRole(c.r.Context(), r.ID)
}

// documentgenerator.role.fillaccesses: accesses — [{roleId, accessCode}, …] — the roles' members anew: the roles not
// given are left with none; «everyone» codes (UA, AU, G2) are the default role's anyway.
func (s *Server) portalDocRoleFill(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "settings"); err != nil {
		return portalOut{}, err
	}
	var items []any
	switch x := portalParam(c.in, "accesses").(type) {
	case []any:
		items = x
	case map[string]any: // accesses[0][roleId]=… of a form
		for _, k := range slices.Sorted(maps.Keys(x)) {
			items = append(items, x[k])
		}
	}
	members := map[string][]string{}
	for _, it := range items {
		m := portalMap(it)
		r, err := s.portalDocRoleOf(c, portalParam(m, "roleId"))
		if err != nil {
			return portalOut{}, err
		}
		ids, err := s.portalAccessIn(c, []string{portalStr(portalParam(m, "accessCode"))})
		if err != nil {
			return portalOut{}, err
		}
		members[r.ID] = append(members[r.ID], ids...)
	}
	if err := s.CRM.SetDocRoleMembers(c.r.Context(), members); err != nil {
		return portalOut{}, err
	}
	s.Log.Info("docgen roles given", "user_id", c.p.UserID, "roles", len(members), "api", true)
	return portalOut{Result: true}, nil
}
