package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/ipaccess"
	"go24/internal/tasks"
)

// ipAll is the picker's choice «Все сотрудники» of a rule of «Ограничение доступа по IP».
const ipAll = "all"

// ipAllowed: the employee works from the request's address by «Ограничение доступа по IP»; a refusal is written in
// «Журнал событий» (once in a while for one person and address, not for every request). A rule that cannot be read
// lets them in: a failing database already stops go24 elsewhere.
func (s *Server) ipAllowed(r *http.Request, userID string) bool {
	if s.IPAccess == nil {
		return true
	}
	ok, err := s.IPAccess.Allowed(r.Context(), userID, audit.ClientIP(r))
	if err != nil {
		s.Log.Warn("ip access", "user_id", userID, "err", err)
		return true
	}
	if !ok {
		s.audit(r, audit.Event{Kind: "ip_denied", UserID: userID, Target: r.Method + " " + r.URL.Path})
	}
	return ok
}

// ipRuleView is a rule of the page: its people and departments as the picker's choices.
type ipRuleView struct {
	ipaccess.Rule
	Sel []tasks.User
}

// ipAccessPage is «Ограничение доступа по IP» of «Настройки» (administrators): the rules — whom, from
// which addresses — a new one, a change, «Удалить».
func (s *Server) ipAccessPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.IPAccess == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	ctx := r.Context()
	rules, err := s.IPAccess.Rules(ctx)
	if err != nil {
		s.fail(w, "ip access", err)
		return
	}
	users, err := s.Tasks.ListUsers(tasks.WithAdmin(ctx))
	if err != nil {
		s.fail(w, "ip access users", err)
		return
	}
	staff := users[:0:0] // guests work in collabs only; the rules are for employees
	for _, u := range users {
		if !u.Guest {
			staff = append(staff, u)
		}
	}
	views := make([]ipRuleView, 0, len(rules))
	for _, rule := range rules {
		sel, err := s.Tasks.DeptChoices(ctx, rule.Depts)
		if err != nil {
			s.fail(w, "ip access depts", err)
			return
		}
		if rule.All {
			sel = append(sel, tasks.User{ID: ipAll})
		}
		for _, id := range rule.Users {
			sel = append(sel, tasks.User{ID: id})
		}
		views = append(views, ipRuleView{Rule: rule, Sel: sel})
	}
	s.render(w, r, "ipaccess", page{TitleKey: "ipaccess.title", Nav: "settings", Data: map[string]any{
		"Rules": views, "Users": staff, "IP": audit.ClientIP(r)}})
}

// ipAccessSave: POST /settings/ip (id=… — that rule, none — a new one; who, nets, title) — a rule saved;
// POST /settings/ip/{id}/delete — removed.
func (s *Server) ipAccessSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.IPAccess == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, "/settings/ip")
		return
	}
	rules, err := s.IPAccess.Rules(r.Context())
	if err != nil {
		s.fail(w, "ip access", err)
		return
	}
	rules = slices.Clone(rules)
	id := r.PathValue("id")
	msg := "ipaccess.deleted"
	if id != "" {
		rules = slices.DeleteFunc(rules, func(x ipaccess.Rule) bool { return x.ID == id })
	} else {
		rule := ipaccess.Rule{ID: r.FormValue("id"), Title: r.FormValue("title"), Nets: []string{r.FormValue("nets")}}
		for _, v := range r.Form["who"] {
			if d, ok := strings.CutPrefix(v, "dept:"); ok {
				rule.Depts = append(rule.Depts, d)
			} else if v == ipAll {
				rule.All = true
			} else if v != "" {
				rule.Users = append(rule.Users, v)
			}
		}
		if i := slices.IndexFunc(rules, func(x ipaccess.Rule) bool { return x.ID == rule.ID && rule.ID != "" }); i >= 0 {
			rules[i] = rule
		} else {
			rule.ID = ""
			rules = append(rules, rule)
		}
		msg = "common.saved"
	}
	if err := s.saveIPRules(r, sess.UserID, rules); err != nil {
		s.taskErr(w, r, err, "/settings/ip")
		return
	}
	http.Redirect(w, r, "/settings/ip?msg="+url.QueryEscape(s.tr(r, msg)), http.StatusSeeOther)
}

// saveIPRules keeps the rules unless they shut out the administrator saving them (from where they are now).
func (s *Server) saveIPRules(r *http.Request, by string, rules []ipaccess.Rule) error {
	for i := range rules { // checked as they will be kept
		nets, err := ipaccess.Parse(strings.Join(rules[i].Nets, "\n"))
		if err != nil {
			return err
		}
		rules[i].Nets = nets
	}
	ip := audit.ClientIP(r)
	ok, err := s.IPAccess.Check(r.Context(), by, ip, rules)
	if err != nil {
		return err
	}
	if !ok {
		return i18n.E("ipaccess.err_self", ip)
	}
	if err := s.IPAccess.Save(r.Context(), by, rules); err != nil {
		return err
	}
	s.audit(r, audit.Event{Kind: "ip_rules_saved", UserID: by})
	return nil
}

// apiIPAccess: GET /api/v1/settings/ip-access — the rules; PUT {"rules": [...]} — all of them replaced (a rule
// without "id" is a new one). Administrators' keys only.
func (s *Server) apiIPAccess(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) || s.IPAccess == nil {
		return i18n.E("err.forbidden")
	}
	if r.Method == http.MethodPut {
		var in struct {
			Rules []ipaccess.Rule `json:"rules"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.saveIPRules(r, p.UserID, in.Rules); err != nil {
			return err
		}
	}
	rules, err := s.IPAccess.Rules(r.Context())
	if err != nil {
		return err
	}
	if rules == nil {
		rules = []ipaccess.Rule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "ip": audit.ClientIP(r)})
	return nil
}
