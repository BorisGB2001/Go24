package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта»: notifications, printing, the source, blocked and safe senders ----------

// As Outlook's: new letters are announced (mail.announce; the settings — /mail/settings?tab=notify) and open pages
// hear of them through the bell's live stream («mail» events: the menu's counter, a pop-up with quick actions, the
// list without reloading); «Печать» of a letter or of its conversation is a clean page of its own; «Показать
// исходный текст» brings the letter from its server; «Заблокировать отправителя» and «Всегда показывать содержимое
// от отправителя» fill the lists of /mail/settings?tab=junk.

func init() {
	mailSettingTabs = append(mailSettingTabs, "notify", "junk")
}

func (s *Server) mailMoreRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /mail/files/{id}/preview", s.requireUser(s.mailFilePreview))
	mux.HandleFunc("GET /mail/files/{id}/raw", s.requireUser(s.mailFileRaw))
	mux.HandleFunc("GET /mail/letters/{letter}/files.zip", s.requireUser(s.mailFilesZip))
	mux.HandleFunc("POST /mail/files/drive", s.requireUser(s.mailFilesToDrive))
	mux.HandleFunc("GET /mail/drive", s.requireUser(s.mailDrive))
	mux.HandleFunc("GET /mail/drive/link", s.requireUser(s.mailDriveLink))
	mux.HandleFunc("POST /mail/drafts/{id}/files/drive", s.requireUser(s.mailDraftDriveFile))
	mux.HandleFunc("GET /mail/letters/{letter}/print", s.requireUser(s.mailPrint))
	mux.HandleFunc("GET /mail/letters/{letter}/source", s.requireUser(s.mailSource))
	mux.HandleFunc("POST /mail/senders/block", s.requireUser(s.mailBlock))
	mux.HandleFunc("POST /mail/senders/safe", s.requireUser(s.mailTrust))
	mux.HandleFunc("POST /mail/senders", s.requireUser(s.mailSenderAdd))
	mux.HandleFunc("POST /mail/senders/delete", s.requireUser(s.mailSenderDrop))
	mux.HandleFunc("POST /mail/settings/notify", s.requireUser(s.mailNotifySave))
	mux.HandleFunc("GET /mail/unread", s.requireUser(s.mailUnread))
}

// mailNewsEvent is a «mail» event of the live stream: the menu's counter and the letters to pop up (mail.News).
func (s *Server) mailNewsEvent(r *http.Request, userID, payload string) string {
	var n mail.News
	_ = json.Unmarshal([]byte(payload), &n)
	unread, _ := s.Mail.Unread(r.Context(), userID)
	data, _ := json.Marshal(map[string]any{"unread": unread, "letters": n.Letters, "sound": n.Sound, "desktop": n.Desktop, "new": n.New})
	return string(data)
}

// mailUnread is the menu's counter (a page asks after it changed the letters itself).
func (s *Server) mailUnread(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	n, err := s.Mail.Unread(r.Context(), sess.UserID)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"unread": n})
}

// mailPrintData is a letter (conv: its conversation) as printed.
type mailPrintData struct {
	Letters []mail.Message
	Files   map[string][]mail.File
	HTML    map[string]template.HTML // the cleaned HTML of each letter (with its own pictures)
	Subject string
}

// mailPrint is «Печать»: the letter (conv=1 — its whole conversation) on a page of its own, with the headers and the
// names of the files, printed at once; pictures from elsewhere only when shown (pics=1) or the sender is safe.
func (s *Server) mailPrint(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, id := r.Context(), r.PathValue("letter")
	list, err := s.Mail.Conversation(ctx, sess.UserID, id)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	d := mailPrintData{Files: map[string][]mail.File{}, HTML: map[string]template.HTML{}}
	pics := r.URL.Query().Get("pics") == "1"
	for _, m := range list {
		if m.ID == id {
			d.Subject = m.Subject
		}
		if r.URL.Query().Get("conv") != "1" && m.ID != id {
			continue
		}
		d.Letters = append(d.Letters, m)
		if d.Files[m.ID], err = s.Mail.Files(ctx, sess.UserID, m.ID); err != nil {
			s.taskErr(w, r, err, "/mail")
			return
		}
		if m.HTML != "" {
			p, err := s.Mail.Pictures(ctx, sess.UserID, m.ID)
			if err != nil {
				s.taskErr(w, r, err, "/mail")
				return
			}
			d.HTML[m.ID] = template.HTML(mail.WithPictures(m.HTML, p)) // cleaned by mail.Sanitize when kept
		}
		if kind, _ := s.Mail.SenderKind(ctx, sess.UserID, m.FromAddr); kind == mail.SenderSafe {
			pics = true
		}
	}
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["mail"].ExecuteTemplate(&buf, "mprint", d); err != nil {
		s.fail(w, "mail print", err)
		return
	}
	img := "'self' data:"
	if pics {
		img = "* data:"
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src "+img+"; style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self' data:")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// mailSource is «Показать исходный текст»: the letter as its server keeps it, as text; download=1 — as a file .eml
// («Сохранить как»).
func (s *Server) mailSource(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("letter")
	raw, err := s.Mail.Source(r.Context(), sess.UserID, id)
	if err != nil {
		var ie *i18n.Error
		if errors.As(err, &ie) && ie.Key == "err.mail_no_source" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(s.errText(r, err)))
			return
		}
		s.taskErr(w, r, err, "/mail")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "message/rfc822")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape("letter.eml")))
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = w.Write(raw)
}

// mailBack is where a form of the mail goes back to.
func mailBack(r *http.Request) string {
	if back := r.FormValue("back"); strings.HasPrefix(back, "/mail") {
		return back
	}
	return "/mail"
}

// mailDone answers a form: the script gets the text, a page goes back with it.
func (s *Server) mailDone(w http.ResponseWriter, r *http.Request, back, text string, extra map[string]any) {
	if fromForm(r) || wantsJSON(r) {
		out := map[string]any{"result": true, "text": text}
		for k, v := range extra {
			out[k] = v
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(text), http.StatusSeeOther)
}

// mailBlock is «Заблокировать отправителя» (domain=1: «Заблокировать домен»).
func (s *Server) mailBlock(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.mailErr(w, r, i18n.E("err.bad_form"), "/mail")
		return
	}
	back := mailBack(r)
	senders, moved, err := s.Mail.BlockSenders(r.Context(), sess.UserID, letterIDs(r.Form["letter"]), r.FormValue("domain") == "1")
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("mail senders blocked", "user_id", sess.UserID, "senders", len(senders), "moved", moved)
	s.Mail.Changed(r.Context(), sess.UserID)
	s.mailDone(w, r, back, s.tr(r, "mail.blocked_done", strings.Join(senders, ", ")), map[string]any{"senders": senders, "moved": moved})
}

// mailUnsubscribe is «Отказаться от подписки» (block=1: and «Заблокировать отправителя»); a newsletter with only
// a page sends the user there (the script opens it in a new tab).
func (s *Server) mailUnsubscribe(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, id, back := r.Context(), r.PathValue("letter"), mailBack(r)
	m, err := s.Mail.Message(ctx, sess.UserID, id)
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	open, err := s.Mail.Unsubscribe(ctx, sess.UserID, id)
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	text, blocked := s.tr(r, "mail.unsub_done", m.From()), false
	if open != "" {
		text = s.tr(r, "mail.unsub_page", m.From())
	}
	if r.FormValue("block") == "1" {
		if _, _, err := s.Mail.BlockSenders(ctx, sess.UserID, []string{id}, false); err != nil {
			s.mailErr(w, r, err, back)
			return
		}
		s.Mail.Changed(ctx, sess.UserID)
		text, blocked = text+" "+s.tr(r, "mail.blocked_done", m.FromAddr), true
	}
	s.Log.Info("mail unsubscribed", "user_id", sess.UserID, "letter", id, "page", open != "", "blocked", blocked)
	if open != "" && !fromForm(r) && !wantsJSON(r) {
		http.Redirect(w, r, open, http.StatusSeeOther)
		return
	}
	s.mailDone(w, r, back, text, map[string]any{"open": open, "blocked": blocked})
}

// mailTrust is «Всегда показывать содержимое от отправителя»: the sender is safe.
func (s *Server) mailTrust(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.mailErr(w, r, i18n.E("err.bad_form"), "/mail")
		return
	}
	back := mailBack(r)
	senders, err := s.Mail.TrustSenders(r.Context(), sess.UserID, letterIDs(r.Form["letter"]))
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.mailDone(w, r, back, s.tr(r, "mail.safe_done", strings.Join(senders, ", ")), map[string]any{"senders": senders})
}

// mailSenderAdd puts an address or a domain on a list of /mail/settings?tab=junk.
func (s *Server) mailSenderAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind := r.FormValue("kind")
	moved, err := s.Mail.AddSender(r.Context(), sess.UserID, kind, r.FormValue("sender"))
	if err != nil {
		s.mailErr(w, r, err, settingsURL("junk"))
		return
	}
	if moved > 0 {
		s.Mail.Changed(r.Context(), sess.UserID)
	}
	http.Redirect(w, r, settingsURL("junk", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailSenderDrop takes an address or a domain off the lists.
func (s *Server) mailSenderDrop(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DropSender(r.Context(), sess.UserID, r.FormValue("sender")); err != nil {
		s.mailErr(w, r, err, settingsURL("junk"))
		return
	}
	http.Redirect(w, r, settingsURL("junk"), http.StatusSeeOther)
}

// mailNotifySave keeps how the user is told of new letters.
func (s *Server) mailNotifySave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	err := s.Mail.SaveNotifyPrefs(r.Context(), sess.UserID, mail.NotifyPrefs{Mode: r.FormValue("notify"), Sound: r.FormValue("sound") == "1",
		Desktop: r.FormValue("desktop") == "1"})
	if err != nil {
		s.mailErr(w, r, err, settingsURL("notify"))
		return
	}
	http.Redirect(w, r, settingsURL("notify", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailSendersData fills the settings' tabs «Уведомления» and «Нежелательная почта».
func (s *Server) mailSendersData(r *http.Request, userID string, d *mailSettingsData) error {
	var err error
	if d.Notify, err = s.Mail.NotifyPrefs(r.Context(), userID); err != nil {
		return err
	}
	d.NotifyModes = mail.NotifyModes
	if d.Blocked, err = s.Mail.Senders(r.Context(), userID, mail.SenderBlocked); err != nil {
		return err
	}
	d.Safe, err = s.Mail.Senders(r.Context(), userID, mail.SenderSafe)
	return err
}
