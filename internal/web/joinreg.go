package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// ---------- «Настройки → Сотрудники»: who invites, «Быстрая регистрация» (/register/{token}) and its requests ----------

// joinLimit: the requests one address may send to the registration page in an hour.
const joinLimit = 5

var joinTries = struct {
	sync.Mutex
	by map[string][]time.Time
}{by: map[string][]time.Time{}}

// joinAllowed counts a request of the address: false — too many in the last hour.
func joinAllowed(ip string, now time.Time) bool {
	joinTries.Lock()
	defer joinTries.Unlock()
	var recent []time.Time
	for _, t := range joinTries.by[ip] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(joinTries.by) > 10000 { // a flood of addresses: forget the old ones
		joinTries.by = map[string][]time.Time{}
	}
	if len(recent) >= joinLimit {
		joinTries.by[ip] = recent
		return false
	}
	joinTries.by[ip] = append(recent, now)
	return true
}

// joinLink is the address of the registration link.
func (s *Server) joinLink(set company.StaffSettings) string {
	if !set.Register || set.Token == "" {
		return ""
	}
	return s.BaseURL + "/register/" + set.Token
}

func (s *Server) staffSettingsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	set, err := s.Company.StaffSettings(r.Context())
	if err != nil {
		s.fail(w, "staff settings", err)
		return
	}
	var flash string
	switch r.URL.Query().Get("saved") {
	case "1":
		flash = s.tr(r, "portal.saved")
	case "link":
		flash = s.tr(r, "staffset.link_renewed")
	}
	s.render(w, r, "staffsettings", page{TitleKey: "staffset.title", Nav: "settings", Flash: flash, Data: map[string]any{
		"S": set, "Link": s.joinLink(set), "Ready": s.Authentik != nil && s.AuthentikPublic != "",
		"Mail": s.inviterMailbox(r.Context(), sess.UserID) != ""}})
}

// staffSettingsSave keeps the form over the stored settings (the token is kept; the one who saved sends the letters).
func (s *Server) staffSettingsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	set, err := s.Company.StaffSettings(r.Context())
	if err == nil {
		set.InviteAll, set.Register, set.Approve = r.FormValue("invite") == "all", r.FormValue("register") != "", r.FormValue("approve") != ""
		set.Domains, err = company.CleanDomains(r.FormValue("domains"))
	}
	if err == nil {
		_, err = s.Company.SaveStaffSettings(r.Context(), set, sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, "/settings/employees")
		return
	}
	s.audit(r, audit.Event{Kind: "staff_saved", UserID: sess.UserID})
	http.Redirect(w, r, "/settings/employees?saved=1", http.StatusSeeOther)
}

// staffLinkRenew: «Обновить ссылку» — the old link stops working.
func (s *Server) staffLinkRenew(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if _, err := s.Company.RenewJoinLink(r.Context(), sess.UserID); err != nil {
		s.taskErr(w, r, err, "/settings/employees")
		return
	}
	s.audit(r, audit.Event{Kind: "join_link_renewed", UserID: sess.UserID})
	http.Redirect(w, r, "/settings/employees?saved=link", http.StatusSeeOther)
}

// registerPage: the page of the registration link for anyone — the name and e-mail to ask to join.
func (s *Server) registerPage(w http.ResponseWriter, r *http.Request) {
	set, err := s.Company.StaffSettings(r.Context())
	if err != nil {
		s.fail(w, "register page", err)
		return
	}
	s.render(w, r, "register", page{TitleKey: "register.title", Data: map[string]any{
		"Open": set.JoinOpen(r.PathValue("token")) && s.Authentik != nil && s.AuthentikPublic != "", "Token": r.PathValue("token"),
		"Domains": set.Domains, "Approve": set.Approve}})
}

// registerSend takes a request of the registration page: with confirmation (or no mailbox to send from) it waits for
// an administrator, without — the sign-up link goes to the e-mail at once, from the mailbox of the one who switched
// the link on; the person learns which.
func (s *Server) registerSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := r.PathValue("token")
	set, err := s.Company.StaffSettings(ctx)
	if err != nil {
		s.fail(w, "register", err)
		return
	}
	data := map[string]any{"Open": set.JoinOpen(token) && s.Authentik != nil && s.AuthentikPublic != "", "Token": token,
		"Domains": set.Domains, "Approve": set.Approve, "Email": r.FormValue("email"), "Name": r.FormValue("name")}
	show := func(problem string) { // the page has no layout's flash for people not signed in
		data["Err"] = problem
		s.render(w, r, "register", page{TitleKey: "register.title", Data: data})
	}
	if data["Open"] != true {
		show("")
		return
	}
	if r.FormValue("website") != "" { // the field people do not see: a robot filled it
		data["Done"] = s.tr(r, "register.waiting", r.FormValue("email"))
		show("")
		return
	}
	ip := audit.ClientIP(r)
	if !joinAllowed(ip, time.Now()) {
		show(s.tr(r, "register.too_many"))
		return
	}
	email, name, err := s.Company.CheckJoin(ctx, set, r.FormValue("email"), r.FormValue("name"))
	if errors.Is(err, company.ErrJoinWaiting) {
		data["Done"] = s.tr(r, "register.already", email)
		show("")
		return
	}
	if err != nil {
		show(s.errText(r, err))
		return
	}
	if !set.Approve && set.By != "" && s.inviterMailbox(ctx, set.By) != "" {
		by, err := s.Company.Profile(ctx, set.By)
		if err == nil {
			var sent []company.Invite
			sent, _, err = s.invite(r, set.By, by.Name, "", []company.Invitee{{Email: email, Name: name}})
			if err == nil && len(sent) == 1 && sent[0].Mailed {
				s.Log.Info("registered by link", "invite", sent[0].ID)
				data["Done"] = s.tr(r, "register.mailed", email)
				show("")
				return
			}
			if err == nil && len(sent) == 1 { // the letter did not go: the link is no use to anyone, an administrator decides
				_, err = s.uninvite(ctx, set.By, sent[0].ID)
			}
		}
		if err != nil {
			s.Log.Error("register by link", "err", err)
		}
	}
	if _, err := s.Company.AddJoinRequest(ctx, email, name, ip, s.AdminGroups); err != nil {
		if _, ok := err.(*i18n.Error); ok {
			show(s.errText(r, err))
			return
		}
		s.fail(w, "join request", err)
		return
	}
	s.Log.Info("join requested", "ip", ip)
	data["Done"] = s.tr(r, "register.waiting", email)
	show("")
}

// joinRequestAccept: «Принять» a request — an invitation of the person, into the department chosen, as the form makes.
func (s *Server) joinRequestAccept(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.canInvite(w, r, sess) {
		return
	}
	inv, err := s.acceptJoin(r, sess.UserID, sess.Name, r.PathValue("id"), r.FormValue("department"))
	if err != nil {
		if _, ok := err.(*i18n.Error); ok || err == company.ErrNotFound {
			s.taskErr(w, r, err, "/company/invite")
		} else {
			s.inviteFailed(w, r, err)
		}
		return
	}
	http.Redirect(w, r, "/company/invite?"+url.Values{"msg": {s.tr(r, "invite.sent", 1, b2i(inv.Mailed))}, "new": {inv.Email}}.Encode(),
		http.StatusSeeOther)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// acceptJoin makes the invitation of a waiting request and closes it.
func (s *Server) acceptJoin(r *http.Request, byID, byName, id, dept string) (company.Invite, error) {
	ctx := r.Context()
	j, err := s.Company.PendingJoin(ctx, id)
	if err != nil {
		return company.Invite{}, err
	}
	if err := s.Company.CheckDepartment(ctx, dept); err != nil {
		return company.Invite{}, err
	}
	email, name, err := s.Company.CheckInvite(ctx, j.Email, j.Name)
	if err != nil {
		return company.Invite{}, err
	}
	sent, _, err := s.invite(r, byID, byName, dept, []company.Invitee{{Email: email, Name: name}})
	if err != nil {
		return company.Invite{}, err
	}
	if err := s.Company.DecideJoin(ctx, id, byID, true, sent[0].ID); err != nil {
		return sent[0], err
	}
	s.Log.Info("join request accepted", "user_id", byID, "id", id)
	return sent[0], nil
}

// joinRequestReject: «Отклонить» — the person gets nothing.
func (s *Server) joinRequestReject(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.canInvite(w, r, sess) {
		return
	}
	j, err := s.Company.PendingJoin(r.Context(), r.PathValue("id"))
	if err == nil {
		err = s.Company.DecideJoin(r.Context(), j.ID, sess.UserID, false, "")
	}
	if err != nil {
		s.taskErr(w, r, err, "/company/invite")
		return
	}
	s.Log.Info("join request rejected", "user_id", sess.UserID, "id", j.ID)
	http.Redirect(w, r, "/company/invite?msg="+url.QueryEscape(s.tr(r, "joinreq.rejected_msg", j.Email))+"#requests", http.StatusSeeOther)
}

// ---------- REST ----------

type apiStaffSettings struct {
	InviteAll bool     `json:"invite_all"`
	Register  bool     `json:"register"`
	Approve   bool     `json:"approve"`
	Domains   []string `json:"domains"`
	Link      string   `json:"link,omitempty"`
}

// GET /api/v1/settings/employees — who invites and «Быстрая регистрация»; PUT {invite_all, register, approve, domains}
// (administrators) — the fields given change; POST …/link — a new registration link.
func (s *Server) apiStaffSettings(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	if !s.apiAdmin(ctx, p.UserID) {
		return i18n.E("err.forbidden")
	}
	set, err := s.Company.StaffSettings(ctx)
	if err != nil {
		return err
	}
	switch {
	case r.Method == http.MethodPost:
		if set, err = s.Company.RenewJoinLink(ctx, p.UserID); err != nil {
			return err
		}
		s.audit(r, audit.Event{Kind: "join_link_renewed", UserID: p.UserID})
	case r.Method == http.MethodPut:
		in := apiStaffSettings{InviteAll: set.InviteAll, Register: set.Register, Approve: set.Approve, Domains: set.Domains}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		set.InviteAll, set.Register, set.Approve = in.InviteAll, in.Register, in.Approve
		if set.Domains, err = company.CleanDomains(strings.Join(in.Domains, " ")); err != nil {
			return err
		}
		if set, err = s.Company.SaveStaffSettings(ctx, set, p.UserID); err != nil {
			return err
		}
		s.audit(r, audit.Event{Kind: "staff_saved", UserID: p.UserID})
	}
	writeJSON(w, http.StatusOK, apiStaffSettings{InviteAll: set.InviteAll, Register: set.Register, Approve: set.Approve,
		Domains: set.Domains, Link: s.joinLink(set)})
	return nil
}

type apiJoinRequest struct {
	ID        string     `json:"id"`
	Email     string     `json:"email"`
	Name      string     `json:"name"`
	IP        string     `json:"ip"`
	State     string     `json:"state"`
	Created   time.Time  `json:"created"`
	DecidedBy string     `json:"decided_by,omitempty"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
	InviteID  string     `json:"invite_id,omitempty"`
}

// GET /api/v1/users/join-requests — the requests of «Быстрая регистрация»: waiting, then decided in the last 30 days.
func (s *Server) apiJoinRequests(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiInviter(r, p); err != nil {
		return err
	}
	list, err := s.Company.JoinRequests(r.Context())
	if err != nil {
		return err
	}
	out := []apiJoinRequest{}
	for _, j := range list {
		out = append(out, apiJoinRequest{j.ID, j.Email, j.Name, j.IP, j.State, j.Created, j.DecidedBy, j.DecidedAt, j.InviteID})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/users/join-requests/{id}/accept {"department_id"} — the invitation (as POST /users/invite makes);
// POST …/reject — the request is closed.
func (s *Server) apiJoinDecide(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	by, err := s.apiInviter(r, p)
	if err != nil {
		return err
	}
	if r.PathValue("op") == "reject" {
		j, err := s.Company.PendingJoin(r.Context(), r.PathValue("id"))
		if err == nil {
			err = s.Company.DecideJoin(r.Context(), j.ID, p.UserID, false, "")
		}
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": j.ID, "state": "rejected"})
		return nil
	}
	if r.PathValue("op") != "accept" {
		return company.ErrNotFound
	}
	var in struct {
		DepartmentID string `json:"department_id"`
	}
	if r.ContentLength > 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	inv, err := s.acceptJoin(r, p.UserID, by, r.PathValue("id"), in.DepartmentID)
	if err != nil {
		return err
	}
	if kept, e := s.Company.InviteByID(r.Context(), inv.ID); e == nil {
		inv = kept
	}
	writeJSON(w, http.StatusOK, apiInviteOf(inv, time.Now()))
	return nil
}
