package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Настройки CoPilot»: the tools where AI works and who may use it (GO24DEV-1005) ----------

// aiOn: the employee sees CoPilot in the tool — AI is set up, they are not a guest and the policy lets them.
func (s *Server) aiOn(ctx context.Context, userID, tool string) bool {
	if s.AI == nil || tasks.IsGuestCtx(ctx) {
		return false
	}
	if err := s.AI.Permit(ctx, userID, tool); err != nil {
		if ie := (*i18n.Error)(nil); !errors.As(err, &ie) {
			s.Log.Warn("ai permit", "err", err)
		}
		return false
	}
	return true
}

// aiPolicyData is the policy for the form of «ИИ»: the tools on, the picker's choices and the employees.
func (s *Server) aiPolicyData(ctx context.Context, p ai.Policy) (map[string]any, error) {
	users, err := s.Tasks.ListUsers(tasks.WithAdmin(ctx))
	if err != nil {
		return nil, err
	}
	staff := users[:0:0]
	for _, u := range users {
		if !u.Guest {
			staff = append(staff, u)
		}
	}
	sel, err := s.Tasks.DeptChoices(ctx, p.Depts)
	if err != nil {
		return nil, err
	}
	if p.Everyone() {
		sel = append(sel, tasks.User{ID: ipAll})
	}
	for _, id := range p.Users {
		sel = append(sel, tasks.User{ID: id})
	}
	return map[string]any{"Tools": ai.Tools, "Off": p.Off, "Sel": sel, "Users": staff}, nil
}

// saveAIPolicy keeps the policy and writes it in «Журнал событий».
func (s *Server) saveAIPolicy(r *http.Request, by string, p ai.Policy) error {
	if err := s.AI.SetPolicy(r.Context(), by, p); err != nil {
		return err
	}
	who := "all"
	if !p.Everyone() {
		who = strings.Join(append(slices.Clone(p.Users), p.Depts...), ", ")
	}
	s.audit(r, audit.Event{Kind: "ai_policy_saved", UserID: by, Target: "off: " + strings.Join(p.Off, ", ") + "; who: " + who})
	return nil
}

// aiPolicySave: POST /settings/ai/policy (on — the tools on, who — employees, dept:… and «all»); administrators.
func (s *Server) aiPolicySave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil || !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/settings/ai")
		return
	}
	var p ai.Policy
	for _, t := range ai.Tools {
		if !slices.Contains(r.Form["on"], t) {
			p.Off = append(p.Off, t)
		}
	}
	all := false
	for _, v := range r.Form["who"] {
		if d, ok := strings.CutPrefix(v, "dept:"); ok {
			p.Depts = append(p.Depts, d)
		} else if v == ipAll {
			all = true
		} else if v != "" {
			p.Users = append(p.Users, v)
		}
	}
	if all { // «Все сотрудники» with others chosen is still everyone
		p.Users, p.Depts = nil, nil
	}
	if err := s.saveAIPolicy(r, sess.UserID, p); err != nil {
		s.taskErr(w, r, err, "/settings/ai")
		return
	}
	http.Redirect(w, r, "/settings/ai?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#ai-policy", http.StatusSeeOther)
}

// apiAIPolicy: GET /api/v1/ai/policy — {tools: [{id, title, on}], users, departments} (empty both — everyone);
// PUT with {off, users, departments} — administrators.
func (s *Server) apiAIPolicy(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiAIOn(r); err != nil {
		return err
	}
	ctx := r.Context()
	if r.Method == http.MethodPut {
		if !s.apiAdmin(ctx, p.UserID) {
			return i18n.E("err.forbidden")
		}
		var in struct {
			Off   []string `json:"off"`
			Users []string `json:"users"`
			Depts []string `json:"departments"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.saveAIPolicy(r, p.UserID, ai.Policy{Off: in.Off, Users: in.Users, Depts: in.Depts}); err != nil {
			return err
		}
	}
	pol, err := s.AI.Policy(ctx)
	if err != nil {
		return err
	}
	type tool struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		On    bool   `json:"on"`
	}
	tools := []tool{}
	for _, t := range ai.Tools {
		tools = append(tools, tool{t, s.tr(r, "ai.tool."+t), !slices.Contains(pol.Off, t)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": tools, "users": nonNil(pol.Users), "departments": nonNil(pol.Depts),
		"allowed": s.AI.Permit(ctx, p.UserID, "") == nil})
	return nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
