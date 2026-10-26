package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта»: rules, automatic replies, «Очистка» ----------

// As Outlook's «Параметры → Почта → Правила / Очистка / Автоматические ответы»: /mail/settings?tab=rules (the rules
// in their order; edit=ID|new, letter=ID — «Создать правило» from a letter, filled with its sender and subject;
// «Запустить сейчас» on a folder), tab=sweep (the standing «Очистка»), tab=ooo (the automatic replies). The
// «Очистка» dialog of letters: /mail/sweep?letter=ID,… (the toolbar's and the letter menu's «Очистка»).

// ruleForm is the rule being edited in the settings.
type ruleForm struct {
	mail.Rule
	Subject string // «Создать правило» from a letter: its subject, offered as a condition
}

// ruleOfForm reads a rule of the form: if_* — the conditions, ex_* — the exceptions, do_* — the actions.
func ruleOfForm(r *http.Request) mail.Rule {
	cond := func(p string) mail.Cond {
		c := mail.Cond{From: r.FormValue(p + "from"), To: r.FormValue(p + "to"), Subject: r.FormValue(p + "subject"), Body: r.FormValue(p + "body"),
			Attach: r.FormValue(p+"attach") != "", Importance: r.FormValue(p + "importance"), ToMe: r.FormValue(p+"to_me") != "",
			CcMe: r.FormValue(p+"cc_me") != "", OnlyMe: r.FormValue(p+"only_me") != ""}
		if strings.TrimSpace(c.Subject) == "" {
			c.Subject = r.FormValue(p + "subject_letter") // the letter's subject ticked
		}
		return c
	}
	return mail.Rule{ID: r.PathValue("id"), Name: r.FormValue("name"), Enabled: r.FormValue("enabled") != "", Stop: r.FormValue("stop") != "",
		If: cond("if_"), Except: cond("ex_"), Do: mail.Do{Move: r.FormValue("do_move"), Category: r.FormValue("do_category"),
			Importance: r.FormValue("do_importance"), Flag: r.FormValue("do_flag") != "", Pin: r.FormValue("do_pin") != "",
			Read: r.FormValue("do_read") != "", Delete: r.FormValue("do_delete") != "", Junk: r.FormValue("do_junk") != "",
			Forward: r.FormValue("do_forward"), ForwardAttach: r.FormValue("do_forward_attach"), Redirect: r.FormValue("do_redirect"),
			Notify: r.FormValue("do_notify") != ""}}
}

// mailRuleEdit is the rule the settings edit: one of the user's, a new one, a new one made of a letter.
func (s *Server) mailRuleEdit(r *http.Request, userID string) (*ruleForm, error) {
	q := r.URL.Query()
	switch id := q.Get("edit"); {
	case id == "":
		return nil, nil
	case id != "new":
		rule, err := s.Mail.Rule(r.Context(), userID, id)
		if err != nil {
			return nil, err
		}
		return &ruleForm{Rule: rule}, nil
	}
	f := &ruleForm{Rule: mail.Rule{Enabled: true}}
	if id := q.Get("letter"); id != "" { // «Создать правило» from a letter, as Outlook: its sender, its subject offered
		list, err := s.Mail.Conversation(r.Context(), userID, id)
		if err != nil {
			return nil, err
		}
		for _, m := range list {
			if m.ID == id {
				f.If.From, f.Subject, f.Name = m.FromAddr, m.Subject, m.Subject
				if f.Name == "" {
					f.Name = m.FromAddr
				}
			}
		}
	}
	return f, nil
}

func (s *Server) mailRuleSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	rule := ruleOfForm(r)
	if _, err := s.Mail.SaveRule(r.Context(), sess.UserID, rule); err != nil {
		edit := rule.ID
		if edit == "" {
			edit = "new"
		}
		s.mailErr(w, r, err, settingsURL("rules", "edit", edit))
		return
	}
	s.Log.Info("mail rule saved", "user_id", sess.UserID, "rule", rule.ID)
	http.Redirect(w, r, settingsURL("rules", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailRuleDo: do=delete, up, down, on, off for a rule of the user.
func (s *Server) mailRuleDo(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, id := r.Context(), r.PathValue("id")
	var err error
	switch r.FormValue("do") {
	case "delete":
		err = s.Mail.DeleteRule(ctx, sess.UserID, id)
	case "up", "down":
		d := 1
		if r.FormValue("do") == "up" {
			d = -1
		}
		err = s.Mail.MoveRule(ctx, sess.UserID, id, d)
	case "on", "off":
		err = s.Mail.EnableRule(ctx, sess.UserID, id, r.FormValue("do") == "on")
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.mailErr(w, r, err, settingsURL("rules"))
		return
	}
	http.Redirect(w, r, settingsURL("rules"), http.StatusSeeOther)
}

// mailRulesRun: «Запустить сейчас» — the rules ticked (none: all that are on) on a folder.
func (s *Server) mailRulesRun(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_ = r.ParseForm()
	n, err := s.Mail.RunRules(r.Context(), sess.UserID, r.FormValue("folder"), r.Form["rule"])
	if err != nil && n == 0 {
		s.mailErr(w, r, err, settingsURL("rules"))
		return
	}
	msg := s.tr(r, "mail.rules_ran", n)
	if err != nil { // some went wrong: what the server said
		msg += " · " + s.errText(r, err)
	}
	s.Log.Info("mail rules run", "user_id", sess.UserID, "folder", r.FormValue("folder"), "letters", n)
	http.Redirect(w, r, settingsURL("rules", "msg", msg), http.StatusSeeOther)
}

// oooOfForm reads the automatic replies of the form (the period in the user's time).
func oooOfForm(r *http.Request) (mail.OOO, error) {
	o := mail.OOO{Mode: r.FormValue("mode"), Internal: r.FormValue("internal"), External: r.FormValue("external"),
		Outside: r.FormValue("outside"), Absence: r.FormValue("absence") != ""}
	if o.Mode == mail.OOOPeriod {
		for _, x := range []struct {
			field string
			to    **time.Time
		}{{"from", &o.From}, {"to", &o.To}} {
			t, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue(x.field), time.Local)
			if err != nil {
				return o, i18n.E("err.mail_ooo_period")
			}
			*x.to = &t
		}
	}
	return o, nil
}

func (s *Server) mailOOOSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	o, err := oooOfForm(r)
	if err == nil {
		err = s.Mail.SaveOOO(r.Context(), sess.UserID, o)
	}
	if err != nil {
		s.mailErr(w, r, err, settingsURL("ooo"))
		return
	}
	s.Log.Info("mail automatic replies", "user_id", sess.UserID, "mode", o.Mode, "absence", o.Absence)
	http.Redirect(w, r, settingsURL("ooo", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailOOOOff: the banner's «Выключить».
func (s *Server) mailOOOOff(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	if err := s.Mail.OOOOff(r.Context(), sess.UserID); err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) mailSweepDrop(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DropSweep(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, settingsURL("sweep"))
		return
	}
	http.Redirect(w, r, settingsURL("sweep"), http.StatusSeeOther)
}

// sweepData is the «Очистка» dialog: the senders of the letters picked, where their letters may go.
type sweepData struct {
	Letters string
	From    []mail.SweepFrom
	Folders []mail.Folder // of the first sender's mailbox
	Back    string
}

// mailSweepPage is the «Очистка» dialog of letters.
func (s *Server) mailSweepPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ids := letterIDs(r.URL.Query()["letter"])
	back := r.URL.Query().Get("back")
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	from, err := s.Mail.SweepPreview(r.Context(), sess.UserID, ids)
	if err == nil && len(from) == 0 {
		err = mail.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	d := &sweepData{Letters: strings.Join(ids, ","), From: from, Back: back}
	folders, err := s.Mail.Folders(r.Context(), sess.UserID, from[0].MailboxID)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	for _, f := range folders {
		if f.Selectable && f.ID != from[0].FolderID && f.Role != "trash" && f.Role != "archive" && f.Role != "drafts" {
			d.Folders = append(d.Folders, f)
		}
	}
	s.render(w, r, "mail", page{TitleKey: "mail.sweep", Nav: "mail", Data: map[string]any{"Sweep": d}})
}

// mailSweep does «Очистка»: letter (ids), mode, target, days, back.
func (s *Server) mailSweep(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_ = r.ParseForm()
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	n, err := s.Mail.SweepNow(r.Context(), sess.UserID, letterIDs(r.Form["letter"]), r.FormValue("mode"), r.FormValue("target"), days)
	if err != nil && n == 0 {
		s.mailErr(w, r, err, "/mail/sweep?"+url.Values{"letter": r.Form["letter"], "back": {back}}.Encode())
		return
	}
	s.Log.Info("mail sweep", "user_id", sess.UserID, "mode", r.FormValue("mode"), "letters", n)
	if u, perr := url.Parse(back); perr == nil { // the letter is gone
		q := u.Query()
		q.Del("letter")
		q.Set("msg", s.tr(r, "mail.swept", n))
		u.RawQuery = q.Encode()
		back = u.String()
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
