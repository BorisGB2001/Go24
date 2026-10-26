package web

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта»: writing a letter ----------

// The editor is Outlook's: a new letter, a reply, reply all or a forward opens in the reading pane (a reply right
// under the letter), keeps itself as a draft a few seconds after each change (static/js/compose.js), takes files at
// once, puts in the mailbox's signature and the user's templates. «Отправить» leaves the seconds of «Отменить»
// (the user's setting) before the letter goes; «Отправить позже» keeps it in «Черновики» till its time.

// schedPreset is a time «Отправить позже» offers.
type schedPreset struct {
	Key string
	At  time.Time
}

// sendPresets are Outlook's: tomorrow morning, tomorrow afternoon, Monday morning.
func sendPresets(now time.Time) []schedPreset {
	out := make([]schedPreset, 0, 3)
	for _, k := range []string{"tomorrow_am", "tomorrow_pm", "monday"} {
		if t, ok := sendTime(k, now); ok {
			out = append(out, schedPreset{k, t})
		}
	}
	return out
}

// sendTime is the time a preset (or a datetime-local value) sends a letter at; false — not a time to come.
func sendTime(v string, now time.Time) (time.Time, bool) {
	day := func(d, hour int) time.Time {
		return time.Date(now.Year(), now.Month(), now.Day()+d, hour, 0, 0, 0, now.Location())
	}
	switch v {
	case "tomorrow_am":
		return day(1, 8), true
	case "tomorrow_pm":
		return day(1, 13), true
	case "monday":
		return day((int(time.Monday)-int(now.Weekday())+6)%7+1, 8), true
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		if t, err = time.ParseInLocation("2006-01-02T15:04", v, now.Location()); err != nil {
			return t, false
		}
	}
	return t, t.After(now)
}

// mailComposeData is what the editor shows.
type mailComposeData struct {
	Draft      mail.Draft
	HTML       string // the editor's text
	Files      []mail.File
	Boxes      []mail.Mailbox
	Froms      []mail.FromAddress // «От»: each mailbox's own address and its aliases
	Signatures []mail.Snippet
	SigOf      map[string]mail.Signatures // the signatures of each mailbox (the script swaps them with «От»)
	Templates  []mail.Snippet
	Prefs      mail.Prefs
	Fonts      []string
	FontSizes  []string
	Schedule   []schedPreset
	Importance []int
	Voting     []string // the voting buttons offered (mail.VotingPresets)
	FolderID   string
	Inline     bool // under the letter in the reading pane
	ShowCC     bool
	ShowBCC    bool
}

// mailCompose prepares the editor: a draft of the user (draft), or a new letter — from a mailbox (box), to
// someone (to), or answering / forwarding a letter (letter, mode; tpl — a template to answer with).
func (s *Server) mailCompose(r *http.Request, sess auth.Session, q url.Values) (*mailComposeData, error) {
	ctx := r.Context()
	boxes, err := s.Mail.Mailboxes(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	if len(boxes) == 0 {
		return nil, i18n.E("err.mail_no_box")
	}
	c := &mailComposeData{Boxes: boxes, Fonts: mail.Fonts, FontSizes: mail.FontSizes, Schedule: sendPresets(time.Now()),
		Importance: []int{1, 0, -1}, Voting: mail.VotingPresets, FolderID: q.Get("folder"), SigOf: map[string]mail.Signatures{}}
	if c.Prefs, err = s.Mail.Prefs(ctx, sess.UserID); err != nil {
		return nil, err
	}
	if c.Signatures, err = s.Mail.Snippets(ctx, sess.UserID, "signature"); err != nil {
		return nil, err
	}
	if c.Templates, err = s.Mail.Snippets(ctx, sess.UserID, "template"); err != nil {
		return nil, err
	}
	for _, b := range boxes {
		if c.SigOf[b.ID], err = s.Mail.MailboxSignatures(ctx, sess.UserID, b.ID); err != nil {
			return nil, err
		}
	}
	if c.Froms, err = s.Mail.FromAddresses(ctx, sess.UserID); err != nil {
		return nil, err
	}
	if id := q.Get("draft"); id != "" {
		if c.Draft, err = s.Mail.Draft(ctx, sess.UserID, id); err != nil {
			return nil, err
		}
		if c.Files, err = s.Mail.Files(ctx, sess.UserID, id); err != nil {
			return nil, err
		}
		c.HTML = c.Draft.HTML
	} else {
		c.Draft = mail.Draft{MailboxID: boxes[0].ID, To: q.Get("to"), Subject: q.Get("subject")}
		for _, b := range boxes {
			if b.ID == q.Get("box") {
				c.Draft.MailboxID = b.ID
			}
		}
		var quote string
		if id := q.Get("letter"); id != "" {
			m, err := s.Mail.Message(ctx, sess.UserID, id)
			if err != nil {
				return nil, err
			}
			pics, err := s.Mail.Pictures(ctx, sess.UserID, m.ID)
			if err != nil {
				return nil, err
			}
			c.Draft.MailboxID, c.Draft.ReplyTo = m.MailboxID, m.ID
			c.Draft.Mode = map[string]string{"all": "all", "fwd": "fwd", "fwdatt": "fwdatt", "resend": "resend"}[q.Get("mode")]
			if c.Draft.Mode == "resend" && m.FolderRole != "sent" {
				c.Draft.Mode = ""
			}
			if c.Draft.Mode == "" {
				c.Draft.Mode = "reply"
			}
			var aliases []string
			for _, f := range c.Froms { // an answer goes from the address the letter came to
				if f.MailboxID == m.MailboxID {
					aliases = append(aliases, f.Email)
					if c.Draft.From == "" && !m.Outgoing() && hasAddr(m.To+","+m.CC, f.Email) {
						c.Draft.From = f.Email
					}
				}
			}
			c.Draft.To, c.Draft.CC, c.Draft.Subject = replyAddrs(m, c.Draft.Mode, aliases...)
			for _, o := range m.Options() { // «Изменить ответ перед отправкой» of a voting button: the answer carries the choice
				if c.Draft.Mode == "reply" && o == q.Get("vote") {
					c.Draft.Vote, c.Draft.Subject = o, o+": "+m.Subject
				}
			}
			switch c.Draft.Mode {
			case "fwdatt": // forwarded as an attachment: the letter is in its file, not quoted
			case "resend": // «Отправить повторно»: the letter itself again, as it went
				c.Draft.From, c.Draft.To, c.Draft.CC, c.Draft.BCC, c.Draft.Subject = m.FromAddr, m.To, m.CC, m.BCC, m.Subject
				c.Draft.Importance, c.Draft.Voting = m.Importance, m.Voting
				if m.HTML != "" {
					quote = mail.WithPictures(m.HTML, pics)
				} else {
					quote = mail.TextHTML(m.Body)
				}
			default:
				quote = s.mailQuote(r, m, pics)
			}
		}
		sig := ""
		sg := c.SigOf[c.Draft.MailboxID]
		if c.Draft.Mode == "resend" { // its signature is in it already
			sg = mail.Signatures{}
		}
		sigID := sg.New
		if c.Draft.Mode != "" {
			sigID = sg.Reply
		}
		for _, x := range c.Signatures {
			if x.ID == sigID {
				sig = `<div data-signature="` + x.ID + `">` + x.HTML + `</div>`
			}
		}
		c.HTML = "<p><br></p>"
		switch c.Draft.Mode {
		case "":
			c.HTML += "<p><br></p>"
		case "resend":
			c.HTML = ""
		}
		if id := q.Get("tpl"); id != "" { // a quick step's reply: its template's text
			tpl, err := s.Mail.Snippet(ctx, sess.UserID, "template", id)
			if err != nil {
				return nil, err
			}
			c.HTML = tpl.HTML
		}
		c.HTML += sig + quote
		if c.Draft.Mode == "fwd" || c.Draft.Mode == "fwdatt" || c.Draft.Mode == "resend" { // a forward is kept at once: the letter's files (or itself) go along
			c.Draft.HTML = c.HTML
			if c.Draft.ID, err = s.Mail.SaveDraft(ctx, sess.UserID, c.Draft); err != nil {
				return nil, err
			}
			if c.Files, err = s.Mail.Files(ctx, sess.UserID, c.Draft.ID); err != nil {
				return nil, err
			}
		}
	}
	c.Draft.From = fromOf(c.Froms, c.Draft, q.Get("draft") == "" && q.Get("letter") == "")
	c.ShowCC, c.ShowBCC = c.Draft.CC != "", c.Draft.BCC != ""
	return c, nil
}

// fromOf is the address of «От» as the list writes it: the draft's when its mailbox may send from it, a new
// letter's — the mailbox's default, otherwise the mailbox's own.
func fromOf(froms []mail.FromAddress, d mail.Draft, isNew bool) string {
	own := ""
	for _, f := range froms {
		if f.MailboxID != d.MailboxID {
			continue
		}
		if own == "" {
			own = f.Email
		}
		if isNew && f.Default || !isNew && d.From != "" && strings.EqualFold(f.Email, d.From) {
			return f.Email
		}
	}
	return own
}

// hasAddr: is the address in the list?
func hasAddr(list, addr string) bool {
	for _, a := range strings.Split(list, ",") {
		a = strings.TrimSpace(a)
		if i := strings.LastIndex(a, "<"); i >= 0 {
			a = strings.TrimSuffix(a[i+1:], ">")
		}
		if strings.EqualFold(a, addr) {
			return true
		}
	}
	return false
}

// replyAddrs: whom an answer goes to and its subject, as Outlook's — reply: the sender (an own sent letter: whom
// it went to); reply all: the sender and the others in «Кому», «Копия» stays the copy; forward (also as an attachment): nobody.
func replyAddrs(m mail.Message, mode string, aliases ...string) (to, cc, subject string) {
	own := strings.ToLower(m.MailboxEmail)
	seen := map[string]bool{own: true}
	for _, a := range aliases { // the mailbox's other addresses are not answered either
		seen[strings.ToLower(a)] = true
	}
	pick := func(lists ...string) string {
		var out []string
		for _, list := range lists {
			for _, a := range strings.Split(list, ",") {
				if a = strings.TrimSpace(a); a != "" && !seen[strings.ToLower(a)] {
					seen[strings.ToLower(a)] = true
					out = append(out, a)
				}
			}
		}
		return strings.Join(out, ", ")
	}
	sender := m.FromAddr
	if m.FromName != "" && !m.Outgoing() {
		sender = (&mailAddr{m.FromName, m.FromAddr}).String()
		seen[strings.ToLower(m.FromAddr)] = true
	}
	prefix := "Re: "
	switch {
	case mode == "fwd" || mode == "fwdatt":
		prefix = "Fwd: "
	case m.Outgoing():
		to = pick(m.To)
		if mode == "all" {
			cc = pick(m.CC)
		}
	default:
		to = sender
		if mode == "all" {
			to = strings.Trim(to+", "+pick(m.To), ", ")
			cc = pick(m.CC)
		}
	}
	subject = m.Subject
	if low := strings.ToLower(subject); !strings.HasPrefix(low, strings.ToLower(prefix)) && !(prefix == "Fwd: " && strings.HasPrefix(low, "fw: ")) {
		subject = prefix + subject
	}
	return to, cc, subject
}

// mailAddr is a name and an address as a header writes them.
type mailAddr struct{ Name, Email string }

func (a *mailAddr) String() string {
	name := a.Name
	if strings.ContainsAny(name, `,;"<>@()`) {
		name = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
	}
	return name + " <" + a.Email + ">"
}

// mailQuote is the letter answered as Outlook quotes it: a line, who it was from, when, to whom, its subject, then
// the letter itself (its HTML with its pictures, or its text).
func (s *Server) mailQuote(r *http.Request, m mail.Message, pics map[string]string) string {
	e := html.EscapeString
	var b strings.Builder
	b.WriteString(`<hr style="display:inline-block;width:98%"><div data-quote="1"><b>` + e(s.tr(r, "mail.from")) + `:</b> ` + e(m.From()))
	if m.FromName != "" {
		b.WriteString(" &lt;" + e(m.FromAddr) + "&gt;")
	}
	b.WriteString(`<br><b>` + e(s.tr(r, "mail.sent_at")) + `:</b> ` + e(m.SentAt.In(time.Local).Format("02.01.2006 15:04")))
	b.WriteString(`<br><b>` + e(s.tr(r, "mail.to")) + `:</b> ` + e(m.To))
	if m.CC != "" {
		b.WriteString(`<br><b>` + e(s.tr(r, "mail.cc")) + `:</b> ` + e(m.CC))
	}
	b.WriteString(`<br><b>` + e(s.tr(r, "mail.subject")) + `:</b> ` + e(m.Subject) + `</div><br>`)
	if m.HTML != "" {
		b.WriteString("<div>" + mail.WithPictures(m.HTML, pics) + "</div>")
	} else {
		b.WriteString("<div>" + mail.TextHTML(m.Body) + "</div>")
	}
	return b.String()
}

// mailComposeHTML is the editor alone, for the script (a reply under the letter, a draft opened from the list).
func (s *Server) mailComposeHTML(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	c, err := s.mailCompose(r, sess, q)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	c.Inline = q.Get("inline") == "1"
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["mail"].ExecuteTemplate(&buf, "mcompose", c); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// draftOf reads the editor's form.
func draftOf(r *http.Request) mail.Draft {
	imp, _ := strconv.Atoi(r.FormValue("importance"))
	return mail.Draft{ID: r.FormValue("draft"), MailboxID: r.FormValue("box"), From: r.FormValue("from"), To: r.FormValue("to"), CC: r.FormValue("cc"), BCC: r.FormValue("bcc"),
		Subject: r.FormValue("subject"), HTML: r.FormValue("html"), Importance: imp, Receipt: r.FormValue("receipt") != "",
		Voting: r.FormValue("voting"), Vote: r.FormValue("vote"),
		Delivery: r.FormValue("delivery") != "", ReplyTo: r.FormValue("reply_to"), Mode: r.FormValue("mode")}
}

// mailDraftSave keeps the letter being written (the editor calls it a few seconds after a change).
func (s *Server) mailDraftSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Mail.SaveDraft(r.Context(), sess.UserID, draftOf(r))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "saved": time.Now().Format("15:04")})
}

// mailDraftSend sends the letter: at the time chosen («Отправить позже»: send_at), after the seconds of «Отменить»,
// or at once when the user turned undo off.
func (s *Server) mailDraftSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	d := draftOf(r)
	if err := d.Check(); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	id, err := s.Mail.SaveDraft(ctx, sess.UserID, d)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := map[string]any{"id": id}
	if v := r.FormValue("send_at"); v != "" {
		at, ok := sendTime(v, time.Now())
		if !ok {
			s.jsonErr(w, r, i18n.E("err.mail_send_at"))
			return
		}
		if err := s.Mail.ScheduleDraft(ctx, sess.UserID, id, at, true); err != nil {
			s.jsonErr(w, r, err)
			return
		}
		s.Log.Info("mail scheduled", "user_id", sess.UserID, "draft", id, "at", at)
		out["scheduled"] = s.tr(r, "mail.scheduled_for", at.In(time.Local).Format("02.01.2006 15:04"))
		writeJSON(w, http.StatusOK, out)
		return
	}
	p, err := s.Mail.Prefs(ctx, sess.UserID)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if p.UndoSeconds > 0 {
		delay := time.Duration(p.UndoSeconds) * time.Second
		if err := s.Mail.ScheduleDraft(ctx, sess.UserID, id, time.Now().Add(delay), false); err != nil {
			s.jsonErr(w, r, err)
			return
		}
		time.AfterFunc(delay+300*time.Millisecond, func() { // the worker would send it too, a little later
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			for _, err := range s.Mail.SendDue(ctx) {
				s.Log.Warn("mail send", "err", err)
			}
		})
		out["undo"] = p.UndoSeconds
		writeJSON(w, http.StatusOK, out)
		return
	}
	if err := s.Mail.SendDraft(ctx, sess.UserID, id); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	s.Log.Info("mail sent", "user_id", sess.UserID, "draft", id)
	out["sent"] = true
	writeJSON(w, http.StatusOK, out)
}

// mailDraftCancel: «Отменить» after «Отправить» (or «Не отправлять» of a scheduled letter) — it stays a draft.
func (s *Server) mailDraftCancel(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Mail.CancelSend(r.Context(), sess.UserID, id); err != nil {
		s.mailErr(w, r, err, "/mail")
		return
	}
	d, err := s.Mail.Draft(r.Context(), sess.UserID, id)
	if err != nil {
		s.mailErr(w, r, err, "/mail")
		return
	}
	href := "/mail?" + url.Values{"box": {d.MailboxID}, "new": {"1"}, "draft": {id}}.Encode()
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, map[string]any{"result": true, "href": href})
		return
	}
	http.Redirect(w, r, href, http.StatusSeeOther)
}

// mailDraftDelete throws the draft away («Отменить» of the editor, «Удалить черновик»).
func (s *Server) mailDraftDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	if err := s.Mail.DeleteDraft(r.Context(), sess.UserID, r.PathValue("id")); err != nil && !errors.Is(err, mail.ErrNotFound) {
		s.mailErr(w, r, err, back)
		return
	}
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// mailDraftFile takes a file for the draft at once: an attachment, or a picture of the text (inline=1).
func (s *Server) mailDraftFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, mail.MaxSendSize+1<<20)
	f, h, err := r.FormFile("file")
	if err != nil {
		s.jsonErr(w, r, i18n.E("err.mail_files", mail.MaxSendFiles))
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, mail.MaxSendSize+1))
	f.Close()
	if err != nil || len(data) > mail.MaxSendSize {
		s.jsonErr(w, r, i18n.E("err.mail_files", mail.MaxSendFiles))
		return
	}
	ct := h.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	file, err := s.Mail.AttachDraft(r.Context(), sess.UserID, r.PathValue("id"),
		mail.Attachment{Name: path.Base(strings.ReplaceAll(h.Filename, "\\", "/")), ContentType: ct, Data: data}, r.FormValue("inline") == "1")
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": file.ID, "name": file.Name, "size": file.Size, "size_text": s.sizeText(r, file.Size),
		"url": "/files/" + file.ID + "?inline=1", "image": file.Image()})
}

func (s *Server) sizeText(r *http.Request, n int64) string {
	return formatSize(func(k string, a ...any) string { return s.tr(r, k, a...) }, n)
}

func (s *Server) mailDraftFileDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DropDraftFile(r.Context(), sess.UserID, r.PathValue("id"), r.PathValue("file")); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
}

// mailRcpt suggests whom to write while the address is typed, as Outlook: the user's contact lists and the
// company's departments (with their people: maillists.go), the user's personal contacts, the people the user wrote
// with, go24's employees, the CRM's contacts, companies and leads the user may see — each marked with its source
// (mailbook.go). people=1 (an @mention) — only people.
func (s *Server) mailRcpt(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, s.mailFind(r.Context(), sess.UserID, q, "", r.URL.Query().Get("people") != "", 12))
}

// mailVote answers the letter's voting button at once («Отправить ответ сейчас»).
func (s *Server) mailVote(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, id := r.Context(), r.PathValue("letter")
	m, err := s.Mail.Message(ctx, sess.UserID, id)
	if err != nil {
		s.mailErr(w, r, err, "/mail")
		return
	}
	if err := s.Mail.Vote(ctx, sess.UserID, id, r.FormValue("choice"), r.FormValue("text")); err != nil {
		s.mailErr(w, r, err, mailURL(m.FolderID, m.ID))
		return
	}
	s.Log.Info("mail vote", "user_id", sess.UserID, "letter", id)
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
		return
	}
	http.Redirect(w, r, mailURL(m.FolderID, m.ID), http.StatusSeeOther)
}

// mailReceiptOf is the read receipt of the letter, in the user's language.
func (s *Server) mailReceiptOf(r *http.Request, m mail.Message) mail.Receipt {
	return mail.Receipt{Subject: s.tr(r, "mail.receipt_subject", m.Subject),
		Text: s.tr(r, "mail.receipt_text", m.Subject, m.SentAt.In(time.Local).Format("02.01.2006 15:04"), time.Now().Format("02.01.2006 15:04"))}
}

// mailReceipt answers a letter that asks for a read receipt: send it, or not (do=decline).
func (s *Server) mailReceipt(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, id := r.Context(), r.PathValue("letter")
	m, err := s.Mail.Message(ctx, sess.UserID, id)
	if err != nil {
		s.mailErr(w, r, err, "/mail")
		return
	}
	if err := s.Mail.SendReceipt(ctx, sess.UserID, id, s.mailReceiptOf(r, m), r.FormValue("do") == "decline"); err != nil {
		s.mailErr(w, r, err, mailURL(m.FolderID, m.ID))
		return
	}
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
		return
	}
	http.Redirect(w, r, mailURL(m.FolderID, m.ID), http.StatusSeeOther)
}

// mailRecall recalls the user's sent letter («Отозвать сообщение»): do=replace — then it opens in the editor again to
// be sent anew («…и заменить новым сообщением»).
func (s *Server) mailRecall(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, id := r.Context(), r.PathValue("letter")
	m, err := s.Mail.Message(ctx, sess.UserID, id)
	if err != nil {
		s.mailErr(w, r, err, "/mail")
		return
	}
	if _, err := s.Mail.Recall(ctx, sess.UserID, id); err != nil {
		s.mailErr(w, r, err, mailURL(m.FolderID, m.ID))
		return
	}
	s.Log.Info("mail recall", "user_id", sess.UserID, "letter", id)
	to := mailURL(m.FolderID, m.ID)
	if r.FormValue("do") == "replace" {
		to = "/mail?" + url.Values{"box": {m.MailboxID}, "new": {"1"}, "letter": {m.ID}, "folder": {m.FolderID}, "mode": {"resend"}}.Encode()
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
