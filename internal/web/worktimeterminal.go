package web

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// «Терминал учёта времени».Time: the terminals in the settings of working time, the PINs of the people
// and the terminal page itself — a tablet at the entrance where people start and finish the working day by PIN.

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// looksUUID: the text is a UUID (a person's id), so the database does not fail on it.
func looksUUID(v string) bool { return uuidRe.MatchString(v) }

func (s *Server) terminalLink(t company.Terminal) string { return s.BaseURL + "/worktime/terminal/" + t.Token }

// terminalsPage: «Настройки → Рабочее время → Терминалы» — for those who change the settings of working time.
func (s *Server) terminalsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.worktimeSettings(r, sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	ctx := r.Context()
	list, err := s.Company.Terminals(ctx)
	if err != nil {
		s.fail(w, "terminals", err)
		return
	}
	links := map[int64]string{}
	for _, t := range list {
		links[t.ID] = s.terminalLink(t)
	}
	users, err := s.Company.Employees(ctx, company.Filter{})
	if err != nil {
		s.fail(w, "terminals", err)
		return
	}
	pins, err := s.Company.WorkPINs(ctx)
	if err != nil {
		s.fail(w, "terminals", err)
		return
	}
	var flash string
	if r.URL.Query().Get("saved") == "1" {
		flash = s.tr(r, "portal.saved")
	}
	s.render(w, r, "wterminals", page{TitleKey: "wterm.title", Nav: "worktime", Flash: flash, Data: map[string]any{
		"Terminals": list, "Links": links, "Users": users, "PINs": pins, "Now": time.Now()}})
}

// terminalsSave: op add (name), save (id, name, active), link (a new link), delete, pin (user, pin; empty — remove).
func (s *Server) terminalsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.worktimeSettings(r, sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	ev := audit.Event{Kind: "worktime_terminal_saved", UserID: sess.UserID, Target: r.FormValue("name")}
	var err error
	switch r.FormValue("op") {
	case "add":
		_, err = s.Company.AddTerminal(ctx, r.FormValue("name"))
	case "save":
		err = s.Company.UpdateTerminal(ctx, id, r.FormValue("name"), r.FormValue("active") != "")
	case "link", "delete":
		var t *company.Terminal
		if t, err = s.Company.Terminal(ctx, id); err == nil && t == nil {
			err = i18n.E("err.not_found")
		}
		if err == nil {
			ev.Target = t.Name
			if r.FormValue("op") == "link" {
				err = s.Company.NewTerminalLink(ctx, id)
			} else {
				err = s.Company.DeleteTerminal(ctx, id)
			}
		}
	case "pin":
		u := r.FormValue("user")
		var p company.Profile
		if !looksUUID(u) {
			err = i18n.E("err.not_found")
		} else if p, err = s.Company.Profile(ctx, u); errors.Is(err, company.ErrNotFound) || err == nil && (p.Guest || !p.Active) {
			err = i18n.E("err.not_found")
		}
		if err == nil {
			err = s.Company.SetWorkPIN(ctx, u, r.FormValue("pin"))
			ev = audit.Event{Kind: "worktime_pin_set", UserID: sess.UserID, Target: p.Name, TargetID: u}
		}
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, "/settings/worktime/terminals")
		return
	}
	s.audit(r, ev)
	http.Redirect(w, r, "/settings/worktime/terminals?saved=1", http.StatusSeeOther)
}

// workPIN: one's own PIN for the terminals (remove — no PIN).
func (s *Server) workPIN(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	pin := r.FormValue("pin")
	if r.FormValue("remove") != "" {
		pin = ""
	} else if pin == "" {
		s.taskErr(w, r, i18n.E("err.wpin_format"), "/worktime#pin")
		return
	}
	if err := s.Company.SetWorkPIN(r.Context(), sess.UserID, pin); err != nil {
		s.taskErr(w, r, err, "/worktime#pin")
		return
	}
	http.Redirect(w, r, "/worktime?msg="+url.QueryEscape(s.tr(r, "wpin.saved"))+"#pin", http.StatusSeeOther)
}

// terminalPage is the terminal itself, for people not signed in: the PIN pad; with a PIN — the person and the
// working-day buttons; with an action — the action done. The page goes back to the PIN pad by itself.
func (s *Server) terminalPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := s.Company.TerminalByToken(ctx, r.PathValue("token"))
	if err != nil {
		s.fail(w, "terminal", err)
		return
	}
	data := map[string]any{"T": t, "Token": r.PathValue("token")}
	show := func(problem string) {
		data["Err"] = problem
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		s.render(w, r, "wterminal", page{TitleKey: "wterm.page", Data: data})
	}
	if t == nil || r.Method != http.MethodPost {
		show("")
		return
	}
	now := time.Now()
	p, err := s.Company.TerminalPerson(ctx, t, r.FormValue("pin"), now)
	if err != nil {
		show(s.errText(r, err))
		return
	}
	data["Person"] = p
	if action := r.FormValue("action"); action != "" {
		at, err := s.workPlace(r, action, nil)
		if err == nil {
			at.Terminal, at.Device = t.Name, "terminal"
			err = s.Company.WorkAction(ctx, p.ID, action, r.FormValue("report"), now, at)
		}
		if err != nil {
			data["Person"] = nil
			show(s.errText(r, err))
			return
		}
		s.Log.Info("terminal workday", "user_id", p.ID, "terminal", t.ID, "action", action)
		s.tellViolations(r, p.ID, action)
		s.Telephony.Kick() // the clients' calls may pass them by now (or not)
		data["Done"] = s.tr(r, "wterm.done."+action, p.Name, now.Format("15:04"))
		show("")
		return
	}
	d, err := s.Company.Today(ctx, p.ID, now)
	if err != nil {
		s.fail(w, "terminal", err)
		return
	}
	rules, err := s.Company.DayRulesOf(ctx, p.ID)
	if err != nil {
		s.fail(w, "terminal", err)
		return
	}
	data["Day"], data["PIN"], data["Now"], data["Rules"] = d, r.FormValue("pin"), now, rules
	show("")
}

// apiTerminalOut is a terminal in REST; the link — for those who may change the settings of working time.
type apiTerminalOut struct {
	ID     int64      `json:"id"`
	Name   string     `json:"name"`
	Active bool       `json:"active"`
	Link   string     `json:"link"`
	UsedAt *time.Time `json:"used_at,omitempty"`
}

// GET|POST /api/v1/worktime/terminals {"name"}, PUT /api/v1/worktime/terminals/{id} {"name","active","new_link"},
// DELETE /api/v1/worktime/terminals/{id} — the time terminals (those who change the settings of working time).
func (s *Server) apiTerminals(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	if ok, err := s.Company.WorktimeSettings(ctx, p.UserID, s.apiAdmin(ctx, p.UserID)); err != nil {
		return err
	} else if !ok {
		return i18n.E("err.forbidden")
	}
	var id int64
	if v := r.PathValue("id"); v != "" {
		var err error
		if id, err = strconv.ParseInt(v, 10, 64); err != nil {
			return i18n.E("err.not_found")
		}
	}
	var in struct {
		Name    string `json:"name"`
		Active  *bool  `json:"active"`
		NewLink bool   `json:"new_link"`
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	var t *company.Terminal
	var err error
	if id != 0 {
		if t, err = s.Company.Terminal(ctx, id); err != nil {
			return err
		} else if t == nil {
			return i18n.E("err.not_found")
		}
	}
	switch r.Method {
	case http.MethodGet:
		if t == nil {
			list, err := s.Company.Terminals(ctx)
			if err != nil {
				return err
			}
			out := []apiTerminalOut{}
			for _, x := range list {
				out = append(out, apiTerminalOut{x.ID, x.Name, x.Active, s.terminalLink(x), x.UsedAt})
			}
			writeJSON(w, http.StatusOK, out)
			return nil
		}
	case http.MethodPost:
		if t, err = s.Company.AddTerminal(ctx, in.Name); err != nil {
			return err
		}
	case http.MethodPut:
		name, active := t.Name, t.Active
		if in.Name != "" {
			name = in.Name
		}
		if in.Active != nil {
			active = *in.Active
		}
		if err := s.Company.UpdateTerminal(ctx, id, name, active); err != nil {
			return err
		}
		if in.NewLink {
			if err := s.Company.NewTerminalLink(ctx, id); err != nil {
				return err
			}
		}
		if t, err = s.Company.Terminal(ctx, id); err != nil {
			return err
		}
	case http.MethodDelete:
		if err := s.Company.DeleteTerminal(ctx, id); err != nil {
			return err
		}
		s.audit(r, audit.Event{Kind: "worktime_terminal_saved", UserID: p.UserID, Target: t.Name})
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	if r.Method != http.MethodGet {
		s.audit(r, audit.Event{Kind: "worktime_terminal_saved", UserID: p.UserID, Target: t.Name})
	}
	writeJSON(w, http.StatusOK, apiTerminalOut{t.ID, t.Name, t.Active, s.terminalLink(*t), t.UsedAt})
	return nil
}
