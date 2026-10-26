package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Sending, as Outlook does it: the text with its HTML (multipart/alternative; the pictures of the text in
// multipart/related), the files, Копия and СК (the blind copy is not in what the others get, only in the sender's
// «Отправленные»), the importance, read and delivery receipts asked for. The letter is kept in «Отправленные».

// Letter is a letter to send.
type Letter struct {
	From              string // the address it goes from: the mailbox's own ("") or one of its aliases (sendas.go)
	To, CC, BCC       string // address lists
	Subject           string
	Text, HTML        string       // HTML "" — a plain text letter; Text "" — made of the HTML
	Files             []Attachment // the files; those with a ContentID are pictures of the HTML
	Importance        int          // -1 low, 0 normal, 1 high
	Receipt, Delivery bool         // ask for a read receipt, a delivery receipt
	Voting, Vote      string       // the voting buttons it asks for («A;B»), the choice an answer votes for (voting.go)
	Refs              []string     // the letters it answers: the conversation's root first, the answered one last
	Sign, Encrypt     bool         // S/MIME: signed with the user's certificate, encrypted to the recipients' (smime.go)
	CRM               []CRMRef     // the CRM cards whose timeline gets it at once (the card's «Письмо»; Entity, EntityID)
	// sent by go24 itself (rules, automatic replies): Auto-Submitted, a Reply-To, a redirect's Resent-From and
	// sender's name, no copy in «Отправленные»
	auto                string
	replyTo, resentFrom *mail.Address
	fromName            string
	noCopy              bool
	calendar            string // an invitation or an answer (iTIP), beside the text as text/calendar (see ics.go)
	unsubscribe         string // a campaign's letter (SendBulk): the address of its «Отписаться»
}

// Limits of a letter sent from go24.
const (
	MaxSendFiles = 10
	MaxSendSize  = 25 << 20
	MaxRcpts     = 100
)

// plainAuth is SMTP AUTH PLAIN that, unlike net/smtp's, also works without TLS — only for a mailbox whose owner
// turned TLS off (a test server).
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}
func (a plainAuth) Next([]byte, bool) ([]byte, error) { return nil, nil }

// Send sends a letter from the user's mailbox and keeps it in «Отправленные».
func (s *Store) Send(ctx context.Context, userID, mailboxID, to, subject, body string, files ...Attachment) error {
	return s.SendReply(ctx, userID, mailboxID, "", to, subject, body, files...)
}

// SendReply sends a plain text letter that answers (or forwards) a letter of the user — it joins that letter's
// conversation (In-Reply-To, References); replyTo "" — a new letter.
func (s *Store) SendReply(ctx context.Context, userID, mailboxID, replyTo, to, subject, body string, files ...Attachment) error {
	refs, err := s.refsOf(ctx, userID, replyTo)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return i18n.E("err.mail_body")
	}
	return s.SendLetter(ctx, userID, mailboxID, Letter{To: to, Subject: subject, Text: body, Files: files, Refs: refs})
}

// Refs are the References of an answer to the user's letter ("" — none).
func (s *Store) Refs(ctx context.Context, userID, replyTo string) ([]string, error) {
	return s.refsOf(ctx, userID, replyTo)
}

// refsOf: see Refs.
func (s *Store) refsOf(ctx context.Context, userID, replyTo string) ([]string, error) {
	if replyTo == "" {
		return nil, nil
	}
	var thread, own string
	if err := s.db.QueryRow(ctx, `SELECT x.thread, x.message_id FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2`, replyTo, userID).Scan(&thread, &own); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var refs []string
	for _, r := range []string{thread, own} {
		if r != "" && (len(refs) == 0 || refs[0] != r) {
			refs = append(refs, r)
		}
	}
	return refs, nil
}

// addresses parses an address list ("" — none).
func addresses(list string) ([]*mail.Address, error) {
	list = strings.Trim(strings.TrimSpace(strings.ReplaceAll(list, ";", ",")), ",")
	if list == "" {
		return nil, nil
	}
	return mail.ParseAddressList(list)
}

func joinAddrs(list []*mail.Address) string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Address
	}
	return strings.Join(out, ", ")
}

// SendLetter sends a letter from the user's mailbox and keeps it in «Отправленные».
func (s *Store) SendLetter(ctx context.Context, userID, mailboxID string, l Letter) error {
	if _, err := s.Mailbox(ctx, userID, mailboxID); err != nil {
		return err
	}
	a, err := s.account(ctx, mailboxID)
	if err != nil {
		return err
	}
	var e envelope
	var errTo, errCC, errBCC error
	e.To, errTo = addresses(l.To)
	e.CC, errCC = addresses(l.CC)
	e.BCC, errBCC = addresses(l.BCC)
	n := len(e.To) + len(e.CC) + len(e.BCC)
	if errTo != nil || errCC != nil || errBCC != nil || n == 0 || n > MaxRcpts {
		return i18n.E("err.mail_to")
	}
	l.Subject = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(l.Subject))
	if l.HTML != "" {
		l.HTML = Sanitize(l.HTML)
		if l.Text == "" {
			l.Text = HTMLText(l.HTML)
		}
	}
	total, attached := 0, 0
	for _, f := range l.Files {
		total += len(f.Data)
		if f.ContentID == "" {
			attached++
		}
	}
	if attached > MaxSendFiles || total > MaxSendSize {
		return i18n.E("err.mail_files", MaxSendFiles)
	}
	if strings.TrimSpace(l.Text) == "" && l.Subject == "" && attached == 0 {
		return i18n.E("err.mail_body")
	}
	if e.From, err = s.sender(ctx, userID, mailboxID, l.From); err != nil {
		return err
	}
	if l.fromName != "" {
		e.From.Name = l.fromName
	}
	e.Host, e.Now = a.Email[strings.LastIndex(a.Email, "@")+1:], time.Now()
	e.Auto, e.ReplyTo, e.ResentFrom = l.auto, l.replyTo, l.resentFrom
	msg, own := compose(e, l)
	var rcpts []string
	for _, list := range [][]*mail.Address{e.To, e.CC, e.BCC} {
		for _, r := range list {
			rcpts = append(rcpts, r.Address)
		}
	}
	e.WithBCC = true // the sender's own copy shows whom it went to blind
	copyMsg, _ := compose(e, l)
	var sec secured
	if l.Sign || l.Encrypt {
		out, x, err := s.secure(ctx, userID, e.From.Address, rcpts, l, e.Now, msg, copyMsg)
		if err != nil {
			return err
		}
		msg, copyMsg, sec = out[0], out[1], x
	}
	if err := sendSMTP(a, rcpts, msg, l.Delivery); err != nil {
		return i18n.E("err.mail_send", err.Error())
	}
	if l.noCopy {
		return nil
	}
	return s.keepSent(ctx, a, copyMsg, sentCopy{From: e.From.Address, To: joinAddrs(e.To), CC: joinAddrs(e.CC), BCC: joinAddrs(e.BCC), Subject: l.Subject,
		Body: l.Text, HTML: l.HTML, Own: own, Thread: threadOf(own, l.Refs), Importance: l.Importance, Voting: l.Voting, Vote: l.Vote, CRM: l.CRM,
		Sign: sec.sign, Signer: sec.signer, Enc: sec.enc}, l.Files)
}

// SendBulk sends a letter of a campaign («Маркетинг») from the user's mailbox: no copy in «Отправленные», the
// address of its «Отписаться» in the List-Unsubscribe header.
func (s *Store) SendBulk(ctx context.Context, userID, mailboxID string, l Letter, unsubscribe string) error {
	l.noCopy, l.unsubscribe = true, unsubscribe
	return s.SendLetter(ctx, userID, mailboxID, l)
}

// sentCopy is what go24 keeps of a sent letter in its own «Отправленные».
type sentCopy struct {
	From, To, CC, BCC, Subject, Body, HTML, Own, Thread string
	Importance                                          int
	Voting, Vote                                        string
	CRM                                                 []CRMRef
	Sign, Signer, Enc                                   string // S/MIME (smime.go)
}

// keepSent puts the sent letter into the server's «Отправленные» (and fetches it from there); a server without
// one — into go24's own «Отправленные».
func (s *Store) keepSent(ctx context.Context, a account, msg []byte, c sentCopy, files []Attachment) error {
	f, err := s.folderByRole(ctx, a.ID, "sent")
	if err != nil {
		return err
	}
	if f.ID != "" && !f.Local {
		return s.remote(ctx, a.ID, func(cl *client.Client, a account) error {
			if err := cl.Append(f.Name, []string{imap.SeenFlag}, time.Now(), bytes.NewReader(msg)); err != nil {
				return err
			}
			if err := s.syncFolder(ctx, cl, a, f); err != nil {
				return err
			}
			return s.linkSent(ctx, a, c)
		})
	}
	if f.ID == "" {
		if err := s.db.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'Sent', 'sent', true)
			ON CONFLICT (mailbox_id, name) DO UPDATE SET role = 'sent' RETURNING id::text`, a.ID).Scan(&f.ID); err != nil {
			return err
		}
	}
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, to_addr, cc_addr, bcc_addr, subject, body,
		body_html, importance, sent_at, seen, message_id, thread, voting, vote, smime_sign, smime_signer, smime_enc)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now(), true, $12, $13, $14, $15, $16, $17, $18)
		RETURNING id`, a.ID, f.ID, time.Now().UnixNano(), c.From, clip(c.To, 2000), clip(c.CC, 2000), clip(c.BCC, 2000), c.Subject, c.Body,
		c.HTML, c.Importance, c.Own, c.Thread, c.Voting, c.Vote, c.Sign, c.Signer, c.Enc).Scan(&id)
	if err != nil {
		return err
	}
	s.bindCRM(ctx, a.UserID, id, false)
	if err := s.linkSent(ctx, a, c); err != nil {
		return err
	}
	if s.Keep == nil {
		return nil
	}
	for _, file := range files { // the copy in «Sent» keeps the files too
		if err := s.Keep(ctx, a.UserID, id, file); err != nil {
			return err
		}
	}
	return nil
}

// envelope is who a letter is from and to.
type envelope struct {
	From         mail.Address
	To, CC, BCC  []*mail.Address
	WithBCC      bool // the sender's own copy
	Host         string
	Now          time.Time
	ID           string // the Message-ID ("" — made of Now and Host)
	Notification bool   // a read receipt (multipart/report)
	ReplyTo      *mail.Address
	Auto         string        // Auto-Submitted: auto-replied (an automatic reply), auto-forwarded (a rule's)
	ResentFrom   *mail.Address // a letter redirected by a rule: who sent it on
}

func addrHeader(list []*mail.Address) string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.String()
	}
	return strings.Join(out, ", ")
}

// compose makes the letter and its Message-ID (as go24 keeps it): the text, with its HTML when there is one, the
// pictures of the HTML next to it, the files around them.
func compose(e envelope, l Letter) ([]byte, string) {
	own := e.ID
	if own == "" {
		own = fmt.Sprintf("%d.go24@%s", e.Now.UnixNano(), e.Host)
	}
	var msg bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&msg, "%s: %s\r\n", k, v) }
	if e.ResentFrom != nil {
		h("Resent-From", e.ResentFrom.String())
		h("Resent-To", addrHeader(e.To))
		h("Resent-Date", e.Now.Format(time.RFC1123Z))
	}
	h("From", e.From.String())
	if e.ReplyTo != nil {
		h("Reply-To", e.ReplyTo.String())
	}
	if len(e.To) > 0 {
		h("To", addrHeader(e.To))
	}
	if len(e.CC) > 0 {
		h("Cc", addrHeader(e.CC))
	}
	if e.WithBCC && len(e.BCC) > 0 {
		h("Bcc", addrHeader(e.BCC))
	}
	h("Subject", mime.QEncoding.Encode("utf-8", l.Subject))
	h("Date", e.Now.Format(time.RFC1123Z))
	h("Message-ID", "<"+own+">")
	h("MIME-Version", "1.0")
	if len(l.Refs) > 0 {
		ids := make([]string, len(l.Refs))
		for i, r := range l.Refs {
			ids[i] = "<" + r + ">"
		}
		h("In-Reply-To", ids[len(ids)-1])
		h("References", strings.Join(ids, " "))
	}
	switch l.Importance {
	case 1:
		h("Importance", "High")
		h("X-Priority", "1 (Highest)")
	case -1:
		h("Importance", "Low")
		h("X-Priority", "5 (Lowest)")
	}
	if l.Receipt {
		h("Disposition-Notification-To", e.From.String())
	}
	if l.Delivery {
		h("Return-Receipt-To", e.From.String()) // servers without DSN
	}
	if l.Voting != "" {
		h(hdrVoting, mime.QEncoding.Encode("utf-8", l.Voting))
	}
	if l.Vote != "" {
		h(hdrVote, mime.QEncoding.Encode("utf-8", l.Vote))
	}
	if e.Auto != "" { // RFC 3834: nobody answers it automatically; Exchange: no out-of-office reply to it
		h("Auto-Submitted", e.Auto)
		h("X-Auto-Response-Suppress", "All")
	}
	if l.unsubscribe != "" { // RFC 2369, 8058: the mail clients' own «Отписаться», in one click
		h("List-Unsubscribe", "<"+l.unsubscribe+">")
		h("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
		h("Precedence", "bulk")
	}
	h("X-Mailer", "go24")
	var files, pictures []Attachment
	for _, f := range l.Files {
		if f.ContentID != "" && l.HTML != "" {
			pictures = append(pictures, f)
		} else if f.ContentID == "" {
			files = append(files, f)
		}
	}
	ct, body := textPart(l.Text)
	if l.HTML != "" {
		hct, hbody := htmlPart(l.HTML)
		if len(pictures) > 0 {
			parts := []part{{ct: hct, cte: "quoted-printable", body: hbody}}
			for _, p := range pictures {
				parts = append(parts, filePart(p, true))
			}
			hct, hbody = multi("related", e.Now, parts, `; type="text/html"`)
		}
		ct, body = multi("alternative", e.Now, []part{{ct: ct, cte: "quoted-printable", body: body}, {ct: hct, cte: cteOf(hct), body: hbody}}, "")
	}
	if l.calendar != "" { // as Outlook: the meeting is another form of the text (calendarPart)
		ct, body = calendarPart(e.Now, l)
	}
	cte := cteOf(ct)
	if len(files) > 0 {
		parts := []part{{ct: ct, cte: cte, body: body}}
		for _, f := range files {
			if e.Notification { // the machine-readable part of a read receipt
				parts = append(parts, part{ct: f.ContentType, cte: "7bit", body: f.Data})
				continue
			}
			if f.ContentType == "message/rfc822" { // a letter attached («Переслать как вложение»): never base64
				x := textproto.MIMEHeader{}
				x.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", mime.QEncoding.Encode("utf-8", f.Name)))
				parts = append(parts, part{ct: "message/rfc822", cte: "7bit", extra: x, body: f.Data})
				continue
			}
			parts = append(parts, filePart(f, false))
		}
		ct, body = multi("mixed", e.Now, parts, "")
		cte = ""
	}
	if e.Notification {
		ct = strings.Replace(ct, "multipart/mixed", "multipart/report; report-type=disposition-notification", 1)
	}
	h("Content-Type", ct)
	if cte != "" {
		h("Content-Transfer-Encoding", cte)
	}
	msg.WriteString("\r\n")
	msg.Write(body)
	return msg.Bytes(), msgID(own)
}

// part is a part of a multipart body.
type part struct {
	ct, cte string
	extra   textproto.MIMEHeader
	body    []byte
}

func cteOf(ct string) string {
	if strings.HasPrefix(ct, "multipart/") {
		return ""
	}
	return "quoted-printable"
}

func qp(s string) []byte {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")))
	_ = w.Close()
	return b.Bytes()
}

func textPart(text string) (string, []byte) {
	return "text/plain; charset=utf-8", qp(text)
}

func htmlPart(h string) (string, []byte) {
	return "text/html; charset=utf-8", qp("<html><head><meta charset=\"utf-8\"></head><body>" + h + "</body></html>")
}

func filePart(f Attachment, inline bool) part {
	name := mime.QEncoding.Encode("utf-8", f.Name)
	disp := "attachment"
	x := textproto.MIMEHeader{}
	if inline {
		disp = "inline"
		x.Set("Content-ID", "<"+f.ContentID+">")
	}
	x.Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disp, name))
	enc := base64.StdEncoding.EncodeToString(f.Data)
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return part{ct: fmt.Sprintf("%s; name=%q", f.ContentType, name), cte: "base64", extra: x, body: []byte(b.String())}
}

// multi joins parts into a multipart body of the kind (mixed, alternative, related, report).
func multi(kind string, now time.Time, parts []part, params string) (string, []byte) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.SetBoundary(fmt.Sprintf("go24-%s-%x", kind, now.UnixNano()))
	for _, p := range parts {
		hd := textproto.MIMEHeader{"Content-Type": {p.ct}}
		if p.cte != "" {
			hd.Set("Content-Transfer-Encoding", p.cte)
		}
		for k, v := range p.extra {
			hd[k] = v
		}
		pw, _ := w.CreatePart(hd)
		_, _ = pw.Write(p.body)
	}
	_ = w.Close()
	return fmt.Sprintf("multipart/%s; boundary=%q%s", kind, w.Boundary(), params), b.Bytes()
}

// smtpCmd sends a command the client has no method for (MAIL and RCPT with DSN parameters).
func smtpCmd(c *smtp.Client, code int, format string, args ...any) error {
	id, err := c.Text.Cmd(format, args...)
	if err != nil {
		return err
	}
	c.Text.StartResponse(id)
	defer c.Text.EndResponse(id)
	_, _, err = c.Text.ReadResponse(code)
	return err
}

// sendSMTP sends the letter; delivery — ask the servers for a delivery receipt (DSN) when the server can.
func sendSMTP(a account, to []string, msg []byte, delivery bool) error {
	addr := net.JoinHostPort(a.SMTPHost, strconv.Itoa(a.SMTPPort))
	cfg := &tls.Config{ServerName: a.SMTPHost, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	var err error
	d := &net.Dialer{Timeout: 15 * time.Second}
	if a.TLS && a.SMTPPort == 465 {
		conn, err = tls.DialWithDialer(d, "tcp", addr, cfg)
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, a.SMTPHost)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if a.TLS && a.SMTPPort != 465 {
		if err := c.StartTLS(cfg); err != nil {
			return err
		}
	}
	if ok, _ := c.Extension("AUTH"); ok {
		if err := c.Auth(plainAuth{a.Login, a.password}); err != nil {
			return err
		}
	}
	dsn := false
	if delivery {
		dsn, _ = c.Extension("DSN")
	}
	if dsn {
		err = smtpCmd(c, 250, "MAIL FROM:<%s> RET=HDRS", a.Email)
	} else {
		err = c.Mail(a.Email)
	}
	if err != nil {
		return err
	}
	for _, r := range to {
		if dsn {
			err = smtpCmd(c, 25, "RCPT TO:<%s> NOTIFY=SUCCESS,FAILURE", r)
		} else {
			err = c.Rcpt(r)
		}
		if err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// Receipt is a read receipt: the subject and the words of its human part (in the user's language).
type Receipt struct{ Subject, Text string }

// SendReceipt sends the read receipt an incoming letter of the user asks for (once); decline — send none and do
// not ask again.
func (s *Store) SendReceipt(ctx context.Context, userID, letterID string, r Receipt, decline bool) error {
	m, err := s.message(ctx, userID, letterID)
	if err != nil {
		return err
	}
	if m.ReceiptTo == "" || m.ReceiptDone {
		return nil
	}
	if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET receipt_done = true WHERE id::text = $1`, m.ID); err != nil || decline {
		return err
	}
	a, err := s.account(ctx, m.MailboxID)
	if err != nil {
		return err
	}
	to, err := mail.ParseAddress(m.ReceiptTo)
	if err != nil {
		return i18n.E("err.mail_to")
	}
	e := envelope{From: mail.Address{Address: a.Email}, To: []*mail.Address{to}, Host: a.Email[strings.LastIndex(a.Email, "@")+1:],
		Now: time.Now(), Notification: true}
	report := "Reporting-UA: go24\r\nFinal-Recipient: rfc822; " + a.Email + "\r\n"
	if m.MessageID != "" {
		report += "Original-Message-ID: <" + m.MessageID + ">\r\n"
	}
	report += "Disposition: manual-action/MDN-sent-manually; displayed\r\n"
	l := Letter{Subject: r.Subject, Text: r.Text, Files: []Attachment{{Name: "MDNPart2.txt", ContentType: "message/disposition-notification",
		Data: []byte(report)}}}
	if m.MessageID != "" {
		l.Refs = []string{m.MessageID}
	}
	msg, _ := compose(e, l)
	if err := sendSMTP(a, []string{to.Address}, msg, false); err != nil {
		return i18n.E("err.mail_send", err.Error())
	}
	return nil
}

// readFile is the bytes of a file of the user's letters (go24 keeps them in its storage: Open).
func (s *Store) readFile(ctx context.Context, userID, fileID string) (Attachment, error) {
	var f Attachment
	var key string
	var storage *string
	var size int64
	err := s.db.QueryRow(ctx, `SELECT f.name, f.content_type, f.content_id, f.object_key, f.storage_id::text, f.size
		FROM files f JOIN mail_messages x ON x.id = f.mail_id JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE f.id::text = $1 AND m.user_id::text = $2`, fileID, userID).Scan(&f.Name, &f.ContentType, &f.ContentID, &key, &storage, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	if err != nil || size == 0 {
		return f, err
	}
	if s.Open == nil {
		return f, errors.New("mail: no file storage")
	}
	r, err := s.Open(ctx, storage, key)
	if err != nil {
		return f, err
	}
	defer r.Close()
	f.Data, err = io.ReadAll(io.LimitReader(r, MaxSendSize+1))
	return f, err
}

// linkSent puts the sent letter (found in «Отправленные» by its Message-ID) into the cards it was written from.
func (s *Store) linkSent(ctx context.Context, a account, c sentCopy) error {
	if len(c.CRM) == 0 {
		return nil
	}
	var id string
	err := s.db.QueryRow(ctx, `SELECT x.id::text FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id
		WHERE x.mailbox_id = $1 AND f.role = 'sent' AND lower(x.message_id) = lower($2) ORDER BY x.sent_at DESC LIMIT 1`, a.ID, c.Own).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, r := range c.CRM {
		if _, err := s.LinkCRM(ctx, a.UserID, id, r.Entity, r.EntityID); err != nil {
			return err
		}
	}
	return nil
}
