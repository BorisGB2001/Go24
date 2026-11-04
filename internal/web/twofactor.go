package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/twofactor"
)

func (s *Server) twoFA() *twofactor.Store { return twofactor.NewStore(s.DB) }

// twoFactorGate is «Двухэтапная авторизация» at a sign-in: a second factor used is noted; without it the policy lets
// in, reminds (the period runs, due — until when) or refuses. groupAdmin — in an administrators' group of Authentik;
// one made an administrator in go24 counts too. Guests work in collabs only: the policy is for employees.
func (s *Server) twoFactorGate(ctx context.Context, userID string, groupAdmin, guest bool, amr []string) (twofactor.Verdict, time.Time, error) {
	if s.DB == nil || guest {
		return twofactor.Pass, time.Time{}, nil
	}
	st := s.twoFA()
	if twofactor.MFA(amr) {
		return twofactor.Pass, time.Time{}, st.Used(ctx, userID)
	}
	p, err := st.Policy(ctx)
	if err != nil || p.Mode == "" {
		return twofactor.Pass, time.Time{}, err
	}
	var admin bool
	var joined time.Time
	if err := s.DB.QueryRow(ctx, `SELECT admin, created_at FROM users WHERE id::text = $1`, userID).Scan(&admin, &joined); err != nil {
		return twofactor.Pass, time.Time{}, err
	}
	v, due := p.Judge(groupAdmin || admin, false, joined, time.Now())
	return v, due, nil
}

// twoFactorPage is «Двухэтапная авторизация» of «Настройки» (administrators): whom the second factor
// is required of, the days to set it up, and the employees — who signed in with it and when, who must until when.
func (s *Server) twoFactorPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	st := s.twoFA()
	p, err := st.Policy(r.Context())
	if err != nil {
		s.fail(w, "two factor", err)
		return
	}
	people, err := st.People(r.Context(), p, s.AdminGroups)
	if err != nil {
		s.fail(w, "two factor people", err)
		return
	}
	with := 0
	for _, x := range people {
		if x.MFAAt != nil {
			with++
		}
	}
	s.render(w, r, "twofactor", page{TitleKey: "twofa.title", Nav: "settings", Flash: r.URL.Query().Get("msg"), Data: map[string]any{
		"Policy": p, "Modes": twofactor.Modes, "DaysChoice": twofactor.DaysChoice, "People": people, "With": with,
		"AccountURL": s.accountURL()}})
}

// twoFactorSave: POST /settings/two-factor (mode, days).
func (s *Server) twoFactorSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	if err := s.saveTwoFactor(r, sess.UserID, twofactor.Policy{Mode: r.FormValue("mode"), Days: days}); err != nil {
		s.taskErr(w, r, err, "/settings/two-factor")
		return
	}
	http.Redirect(w, r, "/settings/two-factor?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// saveTwoFactor keeps the policy unless it shuts out at once the administrator saving it: required of them, no days to
// set up, and they never signed in with a second factor.
func (s *Server) saveTwoFactor(r *http.Request, by string, p twofactor.Policy) error {
	ctx := r.Context()
	var mfa bool
	if err := s.DB.QueryRow(ctx, `SELECT mfa_at IS NOT NULL FROM users WHERE id::text = $1`, by).Scan(&mfa); err != nil {
		return err
	}
	if p.For(true) && p.Days == 0 && !mfa {
		return i18n.E("twofa.err_self")
	}
	changed, err := s.twoFA().Save(ctx, by, p)
	if err != nil {
		return err
	}
	if changed {
		target := p.Mode
		if target != "" {
			target += " " + strconv.Itoa(p.Days)
		}
		s.audit(r, audit.Event{Kind: "twofa_saved", UserID: by, Target: target})
	}
	return nil
}

// twoFactorRemind: «Подключите двухэтапную авторизацию до …» after a sign-in without it while the period runs —
// set it up in Authentik or «Позже» (next — the page the person was going to).
func (s *Server) twoFactorRemind(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	next := r.URL.Query().Get("next")
	if !localPath(next) {
		next = "/my"
	}
	st := s.twoFA()
	p, err := st.Policy(r.Context())
	if err != nil {
		s.fail(w, "two factor", err)
		return
	}
	var admin bool
	var joined time.Time
	var mfaAt *time.Time
	if err := s.DB.QueryRow(r.Context(), `SELECT admin, created_at, mfa_at FROM users WHERE id::text = $1`, sess.UserID).Scan(&admin, &joined, &mfaAt); err != nil {
		s.fail(w, "two factor", err)
		return
	}
	if !p.For(admin || sess.Admin) {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	s.render(w, r, "twofactor_need", page{TitleKey: "twofa.need_title", Data: map[string]any{
		"Due": p.Deadline(joined), "Remind": true, "Next": next, "MFAAt": mfaAt, "AccountURL": s.accountURL()}})
}

// apiTwoFactor: GET /api/v1/settings/two-factor — the policy and the employees; PUT {"mode": "", "admins" or "all",
// "days": N}. Administrators' keys only.
func (s *Server) apiTwoFactor(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	if r.Method == http.MethodPut {
		var in twofactor.Policy
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.saveTwoFactor(r, p.UserID, twofactor.Policy{Mode: in.Mode, Days: in.Days}); err != nil {
			return err
		}
	}
	st := s.twoFA()
	pol, err := st.Policy(r.Context())
	if err != nil {
		return err
	}
	people, err := st.People(r.Context(), pol, s.AdminGroups)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": pol, "users": people})
	return nil
}
