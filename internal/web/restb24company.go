package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/audit"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// REST compatible with the portal (GO24DEV-859): employees and departments — user.fields, user.search, user.add,
// user.update, department.fields, department.add, department.update, department.delete. Employees and departments
// go by their numbers (migration 601); the rights are those of the pages: inviting — who may invite, the card — the
// employee or an administrator, departments — «Права доступа» of the structure.
//
// user.add invites by e-mail as «Пригласить сотрудника» does, and the portal gives the new employee's number at once:
// so the employee is there before they sign up — a record with oidc_sub 'legacy:invite:<invitation>' in the
// departments given, which the first sign-in with that e-mail takes over, as it takes over the employees imported
//.

func init() {
	for name, m := range map[string]portalMethod{
		"user.fields":       {Run: (*Server).portalUserFields},
		"user.search":       {Scope: "company:read", Run: (*Server).portalUserSearch},
		"user.add":          {Scope: "company:write", Run: (*Server).portalUserAdd},
		"user.update":       {Scope: "company:write", Run: (*Server).portalUserUpdate},
		"department.fields": {Scope: "company:read", Run: (*Server).portalDepartmentFields},
		"department.add":    {Scope: "company:write", Run: (*Server).portalDepartmentAdd},
		"department.update": {Scope: "company:write", Run: (*Server).portalDepartmentUpdate},
		"department.delete": {Scope: "company:write", Run: (*Server).portalDepartmentDelete},
	} {
		portalMethods[name] = m
	}
}

var portalUserFieldNames = []string{"ID", "XML_ID", "ACTIVE", "NAME", "LAST_NAME", "SECOND_NAME", "EMAIL", "LAST_LOGIN",
	"TIME_ZONE", "PERSONAL_GENDER", "PERSONAL_PHOTO", "PERSONAL_BIRTHDAY", "PERSONAL_MOBILE", "PERSONAL_CITY",
	"PERSONAL_WWW", "WORK_PHONE", "WORK_POSITION", "UF_PHONE_INNER", "UF_DEPARTMENT", "UF_EMPLOYMENT_DATE", "IS_ONLINE",
	"USER_TYPE"}

// portalTitles: name → its title from the locale (prefix + name), as user.fields and department.fields give them.
func (s *Server) portalTitles(c portalCall, prefix string, names []string) map[string]string {
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := make(map[string]string, len(names))
	for _, n := range names {
		out[n] = s.I18n.T(lang, prefix+n)
	}
	return out
}

// user.fields: the fields of an employee and their titles.
func (s *Server) portalUserFields(c portalCall) (portalOut, error) {
	return portalOut{Result: s.portalTitles(c, "portal.user.", portalUserFieldNames)}, nil
}

// user.search: user.get with the conditions in FILTER or at the top, and FIND — a part of the name, e-mail or position.
func (s *Server) portalUserSearch(c portalCall) (portalOut, error) {
	filter := portalMap(portalParam(c.in, "filter"))
	if filter == nil {
		filter = map[string]any{}
		for k, v := range c.in {
			switch strings.ToUpper(k) {
			case "SORT", "ORDER", "START", "ADMIN_MODE":
			default:
				filter[k] = v
			}
		}
	}
	return s.portalUsers(c, filter)
}

// portalNumOut answers the number of what was added, as user.add and department.add do.
func portalNumOut(n int64) portalOut { return portalOut{Result: n} }

// portalDeptIDs: the departments of UF_DEPARTMENT (numbers) → their ids.
func (s *Server) portalDeptIDs(c portalCall, v any) ([]string, error) {
	var out []string
	for _, x := range portalList(v) {
		id, err := s.portalNum(c.r.Context(), "departments", x)
		if err != nil || id == "" {
			return nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "UF_DEPARTMENT")
		}
		out = append(out, id)
	}
	return out, nil
}

// portalProfileIn lays the fields over the card; set — whether any card field was given.
func (s *Server) portalProfileIn(c portalCall, in map[string]any, u *company.ProfileUpdate) (set bool, err error) {
	for k, v := range in {
		str := strings.TrimSpace(portalStr(v))
		key := strings.ToUpper(k)
		field := map[string]*string{"NAME": &u.FirstName, "LAST_NAME": &u.LastName, "SECOND_NAME": &u.SecondName,
			"WORK_POSITION": &u.Position, "WORK_PHONE": &u.WorkPhone, "PERSONAL_MOBILE": &u.MobilePhone,
			"PERSONAL_GENDER": &u.Gender, "PERSONAL_CITY": &u.City, "PERSONAL_WWW": &u.Website,
			"UF_PHONE_INNER": &u.InnerPhone, "PERSONAL_NOTES": &u.About}[key]
		if field != nil {
			*field, set = str, true
			continue
		}
		switch key {
		case "PERSONAL_BIRTHDAY", "UF_EMPLOYMENT_DATE":
			var t *time.Time
			if str != "" {
				if t, err = portalDateTime(str); err != nil {
					return set, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", key)
				}
			}
			if key == "PERSONAL_BIRTHDAY" {
				u.Birthday = t
			} else {
				u.Employment = t
			}
			set = true
		}
	}
	return set, nil
}

// portalUserUFIn are the values of the profile's user fields (UF_USR_*) given to user.add or user.update: field id →
// value, "" clears.
func (s *Server) portalUserUFIn(c portalCall) (map[string]string, error) {
	ufs, err := s.portalUFs(c.r.Context(), "user", crm.FieldSight{})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, u := range ufs {
		v, ok := portalParamOK(c.in, u.Key())
		if !ok {
			continue
		}
		if out[u.ID], err = portalUFValue(u, v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func profileUpdateOf(p company.Profile) company.ProfileUpdate {
	return company.ProfileUpdate{LastName: p.LastName, FirstName: p.FirstName, SecondName: p.SecondName, Position: p.Position,
		WorkPhone: p.WorkPhone, MobilePhone: p.MobilePhone, About: p.About, Gender: p.Gender, City: p.City,
		InnerPhone: p.InnerPhone, Telegram: p.Telegram, WhatsApp: p.WhatsApp, Website: p.Website, Birthday: p.Birthday,
		Employment: p.Employment, BirthdayYear: p.BirthdayYear}
}

// user.add: invites an employee by EMAIL (NAME, LAST_NAME and the card fields, UF_DEPARTMENT — numbers) → their number.
// Guests (EXTRANET=Y) are invited from a collab, not here.
func (s *Server) portalUserAdd(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	by, err := s.apiInviter(c.r, c.p)
	if err != nil {
		return portalOut{}, err
	}
	if strings.EqualFold(portalStr(portalParam(c.in, "extranet")), "Y") {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_extranet")
	}
	email := strings.ToLower(strings.TrimSpace(portalStr(portalParam(c.in, "email"))))
	if email == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "EMAIL")
	}
	var added bool // added by user.add before, not signed up yet: the portal says the e-mail is taken too
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = $1 AND oidc_sub LIKE 'legacy:invite:%')`,
		email).Scan(&added); err != nil {
		return portalOut{}, err
	}
	if added {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.invite_exists")
	}
	depts, err := s.portalDeptIDs(c, portalParam(c.in, "uf_department"))
	if err != nil {
		return portalOut{}, err
	}
	var u company.ProfileUpdate
	if _, err := s.portalProfileIn(c, c.in, &u); err != nil {
		return portalOut{}, err
	}
	uf, err := s.portalUserUFIn(c)
	if err != nil {
		return portalOut{}, err
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name, _, _ = strings.Cut(email, "@")
	}
	people, err := s.Company.ParseInvitees(ctx, email+" "+strings.ReplaceAll(name, "\n", " "))
	if err != nil {
		return portalOut{}, err
	}
	first := ""
	if len(depts) > 0 {
		first = depts[0]
	}
	if err := s.Company.CheckDepartment(ctx, first); err != nil {
		return portalOut{}, err
	}
	sent, _, err := s.invite(c.r, c.p.UserID, by, first, people)
	if len(sent) == 0 {
		return portalOut{}, err
	}
	// the record the sign-up takes over: one imported with this e-mail, or a new one
	var id string
	var num int64
	err = s.DB.QueryRow(ctx, `SELECT id::text, num FROM users WHERE lower(email) = $1 AND oidc_sub LIKE 'legacy:%' LIMIT 1`, email).Scan(&id, &num)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.DB.QueryRow(ctx, `INSERT INTO users (oidc_sub, email, name) VALUES ('legacy:invite:' || $1, $2, $3) RETURNING id::text, num`,
			sent[0].ID, email, name).Scan(&id, &num)
	}
	if err != nil {
		return portalOut{}, err
	}
	if u.FirstName == "" && u.LastName == "" {
		u.FirstName = name
	}
	if err := s.Company.SaveProfile(ctx, id, u); err != nil {
		return portalOut{}, err
	}
	if err := s.Tasks.SetUserFieldValues(ctx, id, uf); err != nil {
		return portalOut{}, err
	}
	if len(depts) > 0 {
		if err := s.Company.SetDepartments(ctx, c.p.UserID, id, depts); err != nil {
			return portalOut{}, err
		}
		s.syncDeptChats(ctx)
	}
	s.Log.Info("rest portal employee invited", "user_id", c.p.UserID, "id", id)
	return portalNumOut(num), nil
}

// user.update: ID and the fields to change — the card and its UF_USR_* (the employee themself or an administrator),
// UF_DEPARTMENT and ACTIVE (dismisses or returns) — administrators.
func (s *Server) portalUserUpdate(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	id, err := s.portalNum(ctx, "users", portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if id == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_id")
	}
	admin := s.apiAdmin(ctx, c.p.UserID)
	if id != c.p.UserID && !admin {
		return portalOut{}, i18n.E("err.forbidden")
	}
	prof, err := s.Company.Profile(ctx, id)
	if err != nil {
		return portalOut{}, err
	}
	u := profileUpdateOf(prof)
	if u.FirstName == "" && u.LastName == "" {
		u.FirstName, u.LastName = prof.EditFirst(), prof.EditLast()
	}
	set, err := s.portalProfileIn(c, c.in, &u)
	if err != nil {
		return portalOut{}, err
	}
	uf, err := s.portalUserUFIn(c)
	if err != nil {
		return portalOut{}, err
	}
	dv, moving := portalParamOK(c.in, "UF_DEPARTMENT")
	av, activating := portalParamOK(c.in, "ACTIVE")
	if (moving || activating) && !admin {
		return portalOut{}, i18n.E("err.forbidden")
	}
	if set {
		if s.Telephony != nil && u.InnerPhone != prof.InnerPhone { // the inner number is a SIP account: administrators give it
			if !admin {
				return portalOut{}, i18n.E("err.forbidden")
			}
			if err := s.Telephony.SetNumber(ctx, id, u.InnerPhone); err != nil {
				return portalOut{}, err
			}
			defer s.Telephony.Kick()
		}
		if err := s.Company.SaveProfile(ctx, id, u); err != nil {
			return portalOut{}, err
		}
		s.syncAuthentik(id)
	}
	if len(uf) > 0 {
		if err := s.Tasks.SetUserFieldValues(ctx, id, uf); err != nil {
			return portalOut{}, err
		}
	}
	if moving {
		depts, err := s.portalDeptIDs(c, dv)
		if err != nil {
			return portalOut{}, err
		}
		if err := s.Company.SetDepartments(ctx, c.p.UserID, id, depts); err != nil {
			return portalOut{}, err
		}
	}
	if activating {
		a := strings.ToUpper(portalStr(av))
		active := a == "Y" || a == "TRUE" || a == "1"
		if active != prof.Active {
			if err := s.Company.SetActive(ctx, c.p.UserID, id, active); err != nil {
				return portalOut{}, err
			}
			kind := "user_fired"
			if active {
				kind = "user_returned"
			}
			s.audit(c.r, audit.Event{Kind: kind, UserID: c.p.UserID, Target: prof.Name, TargetID: id})
			s.Matrix.Wake()    // a dismissed employee's Matrix account is closed right away
			s.Telephony.Kick() // and their SIP accounts
		}
	}
	if moving || activating {
		s.syncDeptChats(ctx)
	}
	s.Log.Info("rest portal employee updated", "user_id", c.p.UserID, "id", id)
	return portalOut{Result: true}, nil
}

var portalDeptFieldNames = []string{"ID", "NAME", "SORT", "PARENT", "UF_HEAD"}

// department.fields: the fields of a department and their titles.
func (s *Server) portalDepartmentFields(c portalCall) (portalOut, error) {
	return portalOut{Result: s.portalTitles(c, "portal.department.", portalDeptFieldNames)}, nil
}

// portalDeptIn reads NAME, PARENT, UF_HEAD and SORT of department.add and department.update; nil — not given.
func (s *Server) portalDeptIn(c portalCall) (name, parent, head, sort *string, err error) {
	ctx := c.r.Context()
	for k, v := range c.in {
		str := strings.TrimSpace(portalStr(v))
		switch key := strings.ToUpper(k); key {
		case "NAME":
			name = &str
		case "SORT":
			if _, e := strconv.Atoi(str); e != nil {
				return nil, nil, nil, nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", key)
			}
			sort = &str
		case "PARENT", "UF_HEAD":
			table := map[string]string{"PARENT": "departments", "UF_HEAD": "users"}[key]
			id, e := s.portalNum(ctx, table, v)
			if e != nil || key == "PARENT" && id == "" {
				return nil, nil, nil, nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", key)
			}
			if key == "PARENT" {
				parent = &id
			} else {
				head = &id
			}
		}
	}
	return name, parent, head, sort, nil
}

func (s *Server) portalDeptSort(c portalCall, id string, sort *string) error {
	if sort == nil {
		return nil
	}
	_, err := s.DB.Exec(c.r.Context(), `UPDATE departments SET sort = $2::int WHERE id::text = $1`, id, *sort)
	return err
}

// department.add: NAME, PARENT (under the company if not given), UF_HEAD, SORT → the new department's number.
func (s *Server) portalDepartmentAdd(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	name, parent, head, sort, err := s.portalDeptIn(c)
	if err != nil {
		return portalOut{}, err
	}
	if name == nil || *name == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "NAME")
	}
	under := ""
	if parent != nil {
		under = *parent
	} else {
		root, _, err := s.apiTree(c.r)
		if err != nil {
			return portalOut{}, err
		}
		if root != nil {
			under = root.ID
		}
	}
	if err := s.structCheck(c.r, c.p.UserID, s.apiAdmin(ctx, c.p.UserID), "create", under); err != nil {
		return portalOut{}, err
	}
	h := ""
	if head != nil {
		h = *head
	}
	id, err := s.Company.CreateDepartment(ctx, under, *name, h)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalDeptSort(c, id, sort); err != nil {
		return portalOut{}, err
	}
	s.syncDeptChats(ctx)
	var num int64
	if err := s.DB.QueryRow(ctx, `SELECT num FROM departments WHERE id::text = $1`, id).Scan(&num); err != nil {
		return portalOut{}, err
	}
	s.Log.Info("rest portal department added", "user_id", c.p.UserID, "id", id)
	return portalNumOut(num), nil
}

// portalDeptOf: the department of ID (its number).
func (s *Server) portalDeptOf(c portalCall) (*company.Department, error) {
	id, err := s.portalNum(c.r.Context(), "departments", portalParam(c.in, "id"))
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_id")
	}
	_, flat, err := s.apiTree(c.r)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(flat, func(d *company.Department) bool { return d.ID == id })
	if i < 0 {
		return nil, i18n.E("err.not_found")
	}
	return flat[i], nil
}

// department.update: ID and the fields to change; a new PARENT moves the department.
func (s *Server) portalDepartmentUpdate(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	d, err := s.portalDeptOf(c)
	if err != nil {
		return portalOut{}, err
	}
	admin := s.apiAdmin(ctx, c.p.UserID)
	if err := s.structCheck(c.r, c.p.UserID, admin, "edit", d.ID); err != nil {
		return portalOut{}, err
	}
	name, parent, head, sort, err := s.portalDeptIn(c)
	if err != nil {
		return portalOut{}, err
	}
	p, n, h := d.ParentID, d.Name, d.HeadID
	if parent != nil && *parent != p {
		if err := s.structCheck(c.r, c.p.UserID, admin, "edit", *parent); err != nil {
			return portalOut{}, err
		}
		p = *parent
	}
	if name != nil {
		n = *name
	}
	if head != nil {
		h = *head
	}
	deputies := make([]string, 0, len(d.Deputies))
	for _, x := range d.Deputies {
		deputies = append(deputies, x.ID)
	}
	if err := s.Company.UpdateDepartment(ctx, d.ID, p, n, h, deputies...); err != nil {
		return portalOut{}, err
	}
	if err := s.portalDeptSort(c, d.ID, sort); err != nil {
		return portalOut{}, err
	}
	s.syncDeptChats(ctx)
	s.Log.Info("rest portal department updated", "user_id", c.p.UserID, "id", d.ID)
	return portalOut{Result: true}, nil
}

// department.delete: ID.
func (s *Server) portalDepartmentDelete(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	d, err := s.portalDeptOf(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.structCheck(c.r, c.p.UserID, s.apiAdmin(ctx, c.p.UserID), "delete", d.ID); err != nil {
		return portalOut{}, err
	}
	if err := s.Company.DeleteDepartment(ctx, d.ID); err != nil {
		return portalOut{}, err
	}
	s.syncDeptChats(ctx)
	s.Log.Info("rest portal department deleted", "user_id", c.p.UserID, "id", d.ID)
	return portalOut{Result: true}, nil
}
