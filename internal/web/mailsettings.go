package web

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта»: settings of writing letters ----------

// As Outlook's «Параметры → Почта → Создание и ответ»: signatures (several, edited in the same editor as letters;
// each mailbox has one for new letters and one for replies and forwards), «Мои шаблоны», the seconds «Отменить» is
// offered after «Отправить», the letter's font, «Отсортированные» on or off with the senders' rules; the rules, the
// standing «Очистка», the automatic replies (mailrules.go).
// «Пересылка»: each mailbox's forwarding of its new letters (mail/forwarding.go).
// «Адреса отправителя»: each mailbox's sender name and the other addresses it sends from (mail/sendas.go).
// «CRM»: each mailbox's «Привязывать письма к CRM» and «Создавать лид для новых адресов».
// «Быстрые действия»: the user's quick steps (mailquick.go). «Списки контактов»: the user's lists (maillists.go).
// «Макет»: the density of the list, the text size, what a row shows (mail.Layout).
// «Безопасность»: the user's S/MIME certificates, signing and encrypting by default (mailsmime.go).
// /mail/settings?tab=signatures|templates|compose|handling|layout|lists|focused|rules|quick|sweep|ooo|forwarding|senders|crm|security.

var mailSettingTabs = []string{"signatures", "templates", "compose", "handling", "layout", "lists", "focused", "rules", "quick", "sweep", "ooo", "forwarding", "senders", "crm", "security"}

type mailSettingsData struct {
	Tab                          string
	Tabs                         []string
	Boxes                        []mail.Mailbox
	SigOf                        map[string]mail.Signatures
	CRMOf                        map[string]mail.BoxCRM
	FwdOf                        map[string]mail.BoxForward
	FromOf                       map[string]mail.BoxSenders
	Froms                        []mail.FromAddress // the addresses a new letter of each mailbox may be from
	UserName                     string
	Signatures                   []mail.Snippet
	Templates                    []mail.Snippet
	Prefs                        mail.Prefs
	Handling                     mail.Handling // «Обработка сообщений»
	Layout                       mail.Layout   // «Макет»
	Densities, TextSizes         []string
	ReadAfters                   []int
	ReceiptModes, AfterMoveModes []string
	Fonts                        []string
	FontSizes                    []string
	Undo                         []int
	Edit                         string // the signature or template being edited ("new" — a new one)
	Rules                        []mail.FocusRule
	MailRules                    []mail.Rule
	Rule                         *ruleForm // the rule being edited
	QuickSteps                   []mail.QuickStep
	QuickStep                    *mail.QuickStep // the quick step being edited
	StepActions                  []string
	MaxSteps                     []int
	Folders                      []mailBox // where a rule moves letters, what «Запустить сейчас» runs on
	FolderByID                   map[string]mail.Folder
	Categories                   []mail.Category
	CatByID                      map[string]mail.Category
	Sweeps                       []mail.Sweep
	OOO                          mail.OOO
	OOOState                     mail.OOOState
	Importances                  []string
	Outside                      []string
	Notify                       mail.NotifyPrefs // how new letters are announced (mailmore.go)
	NotifyModes                  []string
	Blocked                      []mail.Sender
	Safe                         []mail.Sender
	Lists                        []mail.ContactList
	List                         *mail.ContactList // the contact list being edited
	Certs                        []mail.Cert       // «Безопасность»
	Security                     mail.Security
}

func (s *Server) mailSettingsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	d := &mailSettingsData{Tab: "signatures", Tabs: mailSettingTabs, SigOf: map[string]mail.Signatures{}, CRMOf: map[string]mail.BoxCRM{}, FwdOf: map[string]mail.BoxForward{}, FromOf: map[string]mail.BoxSenders{}, UserName: sess.Name, Fonts: mail.Fonts,
		FontSizes: mail.FontSizes, Undo: []int{0, 5, 10}, Edit: r.URL.Query().Get("edit"),
		ReadAfters: mail.ReadAfters, ReceiptModes: mail.ReceiptModes, AfterMoveModes: mail.AfterMoveModes,
		Densities: mail.Densities, TextSizes: mail.TextSizes}
	for _, t := range mailSettingTabs {
		if r.URL.Query().Get("tab") == t {
			d.Tab = t
		}
	}
	var err error
	if d.Boxes, err = s.Mail.Mailboxes(ctx, sess.UserID); err == nil {
		for _, b := range d.Boxes {
			if d.SigOf[b.ID], err = s.Mail.MailboxSignatures(ctx, sess.UserID, b.ID); err != nil {
				break
			}
			if d.CRMOf[b.ID], err = s.Mail.MailboxCRM(ctx, sess.UserID, b.ID); err != nil {
				break
			}
			if d.FwdOf[b.ID], err = s.Mail.MailboxForward(ctx, sess.UserID, b.ID); err != nil {
				break
			}
			if d.FromOf[b.ID], err = s.Mail.MailboxSenders(ctx, sess.UserID, b.ID); err != nil {
				break
			}
		}
		if err == nil && d.Tab == "senders" {
			d.Froms, err = s.Mail.FromAddresses(ctx, sess.UserID)
		}
	}
	if err == nil {
		d.Signatures, err = s.Mail.Snippets(ctx, sess.UserID, "signature")
	}
	if err == nil {
		d.Templates, err = s.Mail.Snippets(ctx, sess.UserID, "template")
	}
	if err == nil {
		d.Prefs, err = s.Mail.Prefs(ctx, sess.UserID)
	}
	if err == nil {
		d.Handling, err = s.Mail.Handling(ctx, sess.UserID)
	}
	if err == nil {
		d.Layout, err = s.Mail.Layout(ctx, sess.UserID)
	}
	if err == nil {
		d.Rules, err = s.Mail.FocusRules(ctx, sess.UserID)
	}
	if err == nil {
		err = s.mailRulesData(r, sess.UserID, d)
	}
	if err == nil {
		err = s.mailSendersData(r, sess.UserID, d)
	}
	if err == nil && d.Tab == "lists" {
		err = s.mailListsData(r, sess.UserID, d)
	}
	if err == nil && d.Tab == "security" {
		err = s.mailSMIMEData(r, sess.UserID, d)
	}
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	s.render(w, r, "mail", page{TitleKey: "mail.settings", Nav: "mail", Data: map[string]any{"Settings": d}})
}

func settingsURL(tab string, extra ...string) string {
	v := url.Values{"tab": {tab}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return "/mail/settings?" + v.Encode()
}

// mailPrefsSave keeps the seconds of «Отменить» and the font.
func (s *Server) mailPrefsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	undo, err := strconv.Atoi(r.FormValue("undo_seconds"))
	if err == nil {
		err = s.Mail.SavePrefs(r.Context(), sess.UserID, mail.Prefs{UndoSeconds: undo, Font: r.FormValue("font"), FontSize: r.FormValue("font_size")})
	} else {
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.mailErr(w, r, err, settingsURL("compose"))
		return
	}
	http.Redirect(w, r, settingsURL("compose", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailHandlingSave keeps «Обработка сообщений».
func (s *Server) mailHandlingSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	read, err := strconv.Atoi(r.FormValue("read_after"))
	if err == nil {
		err = s.Mail.SaveHandling(r.Context(), sess.UserID, mail.Handling{ReadAfter: read, Receipts: r.FormValue("receipts"), AfterMove: r.FormValue("after_move")})
	} else {
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.mailErr(w, r, err, settingsURL("handling"))
		return
	}
	http.Redirect(w, r, settingsURL("handling", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailBoxSignatures sets the mailbox's signatures for new letters and for replies.
func (s *Server) mailBoxSignatures(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.SetSignatures(r.Context(), sess.UserID, r.PathValue("id"), mail.Signatures{New: r.FormValue("sig_new"),
		Reply: r.FormValue("sig_reply")}); err != nil {
		s.mailErr(w, r, err, settingsURL("signatures"))
		return
	}
	http.Redirect(w, r, settingsURL("signatures"), http.StatusSeeOther)
}

// mailBoxCRM sets the mailbox's binding of letters to the CRM.
func (s *Server) mailBoxCRM(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.SetMailboxCRM(r.Context(), sess.UserID, r.PathValue("id"), mail.BoxCRM{Bind: r.FormValue("crm_bind") == "1",
		Leads: r.FormValue("crm_leads") == "1"}); err != nil {
		s.mailErr(w, r, err, settingsURL("crm"))
		return
	}
	http.Redirect(w, r, settingsURL("crm", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailBoxForward sets the mailbox's «Пересылка».
func (s *Server) mailBoxForward(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.SetMailboxForward(r.Context(), sess.UserID, r.PathValue("id"), mail.BoxForward{On: r.FormValue("forward_on") == "1",
		To: r.FormValue("forward_to"), Keep: r.FormValue("forward_keep") == "1"}); err != nil {
		s.mailErr(w, r, err, settingsURL("forwarding"))
		return
	}
	http.Redirect(w, r, settingsURL("forwarding", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailBoxSenders sets the mailbox's «Адреса отправителя».
func (s *Server) mailBoxSenders(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.SetMailboxSenders(r.Context(), sess.UserID, r.PathValue("id"), mail.BoxSenders{Name: r.FormValue("sender_name"),
		SendAs: r.FormValue("send_as"), Default: r.FormValue("send_default")}); err != nil {
		s.mailErr(w, r, err, settingsURL("senders"))
		return
	}
	http.Redirect(w, r, settingsURL("senders", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

func snippetKind(r *http.Request) (string, bool) {
	k := r.PathValue("kind")
	return k, k == "signature" || k == "template"
}

func snippetTab(kind string) string {
	if kind == "template" {
		return "templates"
	}
	return "signatures"
}

// mailSnippetSave makes or changes a signature or a template (the editor's «Сохранить как шаблон» too: JSON).
func (s *Server) mailSnippetSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, ok := snippetKind(r)
	if !ok {
		s.mailErr(w, r, mail.ErrNotFound, "/mail/settings")
		return
	}
	id, err := s.Mail.SaveSnippet(r.Context(), sess.UserID, kind, r.PathValue("id"), r.FormValue("name"), r.FormValue("html"))
	if err != nil {
		if r.Header.Get("Accept") == "application/json" {
			s.jsonErr(w, r, err)
			return
		}
		s.mailErr(w, r, err, settingsURL(snippetTab(kind), "edit", "new"))
		return
	}
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": r.FormValue("name")})
		return
	}
	http.Redirect(w, r, settingsURL(snippetTab(kind)), http.StatusSeeOther)
}

func (s *Server) mailSnippetDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, ok := snippetKind(r)
	if !ok {
		s.mailErr(w, r, mail.ErrNotFound, "/mail/settings")
		return
	}
	if err := s.Mail.DeleteSnippet(r.Context(), sess.UserID, kind, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, settingsURL(snippetTab(kind)))
		return
	}
	http.Redirect(w, r, settingsURL(snippetTab(kind)), http.StatusSeeOther)
}

// mailRulesData fills what the tabs of the rules, «Очистка» and the automatic replies show.
func (s *Server) mailRulesData(r *http.Request, userID string, d *mailSettingsData) error {
	ctx := r.Context()
	var err error
	d.Importances, d.Outside, d.FolderByID = mail.Importances, mail.OOOOutside, map[string]mail.Folder{}
	switch d.Tab {
	case "rules", "quick":
		if d.Tab == "rules" {
			if d.MailRules, err = s.Mail.Rules(ctx, userID); err != nil {
				return err
			}
			if d.Rule, err = s.mailRuleEdit(r, userID); err != nil {
				return err
			}
		} else if err = s.mailQuickData(r, userID, d); err != nil {
			return err
		}
		if d.Categories, err = s.mailCategories(r, userID); err != nil {
			return err
		}
		d.CatByID = catMap(d.Categories)
		folders, err := s.Mail.Folders(ctx, userID, "")
		if err != nil {
			return err
		}
		for _, b := range d.Boxes {
			mb := mailBox{Mailbox: b}
			for _, f := range folders {
				if f.MailboxID == b.ID {
					mb.Folders = append(mb.Folders, f)
				}
			}
			d.Folders = append(d.Folders, mb)
		}
		for _, f := range folders {
			d.FolderByID[f.ID] = f
		}
	case "sweep":
		d.Sweeps, err = s.Mail.Sweeps(ctx, userID)
		if err == nil {
			var folders []mail.Folder
			if folders, err = s.Mail.Folders(ctx, userID, ""); err == nil {
				for _, f := range folders {
					d.FolderByID[f.ID] = f
				}
			}
		}
	case "ooo":
		d.OOO, d.OOOState, err = s.Mail.OOONow(ctx, userID, time.Now())
	}
	return err
}

// mailLayoutSave keeps «Макет».
func (s *Server) mailLayoutSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	err := s.Mail.SaveLayout(r.Context(), sess.UserID, mail.Layout{Density: r.FormValue("density"), TextSize: r.FormValue("text_size"),
		ShowText: r.FormValue("show_text") == "1", ShowPics: r.FormValue("show_pics") == "1", ShowFiles: r.FormValue("show_files") == "1"})
	if err != nil {
		s.mailErr(w, r, err, settingsURL("layout"))
		return
	}
	http.Redirect(w, r, settingsURL("layout", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}
