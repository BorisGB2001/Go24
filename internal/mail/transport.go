package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	gomail "github.com/emersion/go-message/mail"
	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// dialIMAP connects to the IMAP server: TLS on 993, STARTTLS elsewhere, plain only when TLS is off (a test server).
func dialIMAP(host string, port int, useTLS bool) (*client.Client, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	d := &net.Dialer{Timeout: 15 * time.Second}
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if useTLS && port == 993 {
		return client.DialWithDialerTLS(d, addr, cfg)
	}
	c, err := client.DialWithDialer(d, addr)
	if err != nil {
		return nil, err
	}
	if useTLS {
		if err := c.StartTLS(cfg); err != nil {
			c.Logout()
			return nil, err
		}
	}
	return c, nil
}

// Probe checks that go24 can sign in to the IMAP server with the settings.
func Probe(st Settings) error {
	if err := st.check(); err != nil {
		return err
	}
	c, err := dialIMAP(st.IMAPHost, st.IMAPPort, st.TLS)
	if err != nil {
		return i18n.E("err.mail_connect", err.Error())
	}
	defer c.Logout()
	if err := c.Login(st.Login, st.Password); err != nil { // the server's own words say why (they never hold the password)
		return i18n.E("err.mail_login_said", clip(err.Error(), 300))
	}
	return nil
}

type account struct {
	Mailbox
	password string
}

func (s *Store) account(ctx context.Context, id string) (account, error) {
	var a account
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT id, user_id::text, email, imap_host, smtp_host, login, imap_port, smtp_port, tls, secret
		FROM mailboxes WHERE id::text = $1`, id).Scan(&a.ID, &a.UserID, &a.Email, &a.IMAPHost, &a.SMTPHost, &a.Login, &a.IMAPPort,
		&a.SMTPPort, &a.TLS, &sealed)
	if err != nil {
		return a, err
	}
	a.password, err = s.open(sealed)
	return a, err
}

// firstFetch is how many of the newest letters of a folder go24 brings the first time.
const firstFetch = 100

// maxUID: IMAP numbers are 32-bit; letters go24 kept itself (sent before the server had «Отправленные») have bigger
// ones and are never dropped by what the server says.
const maxUID = 1<<32 - 1

// maxReconcile: folders up to this size have their flags and deleted letters brought from the server each time.
const maxReconcile = 20000

// Sync fetches a mailbox: its folder list, then the new letters of every folder, and what changed on the server
// (read, deleted, moved away). A failure is kept on the mailbox for its owner to see.
func (s *Store) Sync(ctx context.Context, id string) error {
	var owner string
	var news []arrived
	changed := false
	err := s.remote(ctx, id, func(c *client.Client, a account) error {
		owner = a.UserID
		if err := s.syncFolders(ctx, c, a.ID); err != nil {
			return err
		}
		if err := s.pushDrafts(ctx, c, a); err != nil { // a draft not put on the server does not keep the letters back
			if ctx.Err() != nil {
				return err
			}
			if s.Log != nil {
				s.Log("mail: drafts to the server", a.ID, err)
			}
		}
		folders, err := s.boxFolders(ctx, a.ID)
		if err != nil {
			return err
		}
		for _, f := range folders {
			got, ch, err := s.fetchFolder(ctx, c, a, f)
			news, changed = append(news, got...), changed || ch
			if err != nil {
				return err
			}
		}
		return nil
	})
	s.landed(ctx, owner, id, news, changed)
	msg := ""
	if err != nil {
		msg = err.Error()
		var ie *i18n.Error
		switch {
		case errors.As(err, &ie):
			msg = ie.Key
		case errors.Is(err, context.DeadlineExceeded): // what was fetched is kept; the rest — the next time
			msg = "err.mail_slow"
		}
	}
	// written even when the time ran out: otherwise the worker would start this mailbox over at once, every minute
	_, uerr := s.db.Exec(context.WithoutCancel(ctx), `UPDATE mailboxes SET synced_at = now(), error = left($2, 500) WHERE id::text = $1`, id, msg)
	if err != nil {
		return err
	}
	return uerr
}

// landed: blocked senders of the new letters go to junk, the letters of ignored conversations to «Удалённые»; what came is answered, forwarded, sorted by the rules and announced,
// as in Outlook; letters read, moved or deleted elsewhere wake the open pages (the counters).
func (s *Store) landed(ctx context.Context, owner, id string, news []arrived, changed bool) {
	if len(news) == 0 {
		if changed && owner != "" {
			s.Changed(ctx, owner)
		}
		return
	}
	var jerr error
	if news, jerr = s.junkBlocked(ctx, owner, news); jerr != nil && s.Log != nil {
		s.Log("mail: blocked senders", id, jerr)
	}
	if news, jerr = s.trashIgnored(ctx, owner, news); jerr != nil && s.Log != nil {
		s.Log("mail: ignored conversations", id, jerr)
	}
	if len(news) > 0 {
		news = s.arrive(ctx, owner, id, news)
	}
	for _, n := range news {
		s.bindCRM(ctx, owner, n.id, n.bulk)
	}
	if aerr := s.announce(ctx, owner, news); aerr != nil && s.Log != nil {
		s.Log("mail: notifications", id, aerr)
	}
}

// boxFolders: the server folders of a mailbox that hold letters, the inbox first, then those waiting longest.
func (s *Store) boxFolders(ctx context.Context, mailboxID string) ([]Folder, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, mailbox_id::text, name, delim, role, selectable, local, 0, 0 FROM mail_folders
		WHERE mailbox_id::text = $1 AND selectable AND NOT local ORDER BY role <> 'inbox', synced_at NULLS FIRST, name`, mailboxID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Folder])
}

// syncFolder fetches the folder's new letters and brings its flags and deletions from the server.
func (s *Store) syncFolder(ctx context.Context, c *client.Client, a account, f Folder) error {
	_, _, err := s.fetchFolder(ctx, c, a, f)
	return err
}

// arrived is a letter new in an inbox (not one of a folder's first fetch).
type arrived struct {
	id   string
	bulk bool // sent to many or by a robot (see Bulk)
	fwd  bool // sent on by a mail program itself (Auto-Submitted: auto-forwarded, Resent-From): not forwarded again
}

// fetchFolder: see syncFolder; the letters new in the inbox are returned, and whether letters already here changed.
func (s *Store) fetchFolder(ctx context.Context, c *client.Client, a account, f Folder) (news []arrived, changed bool, _ error) {
	var last, validity int64
	if err := s.db.QueryRow(ctx, `SELECT last_uid, validity FROM mail_folders WHERE id::text = $1`, f.ID).Scan(&last, &validity); err != nil {
		return nil, false, err
	}
	box, err := c.Select(f.Name, true) // read-only: fetching does not mark letters read
	if err != nil {
		return nil, false, err
	}
	lastUID := uint32(last)
	if validity != 0 && box.UidValidity != uint32(validity) { // the server renumbered the folder: start over
		if _, err := s.db.Exec(ctx, `DELETE FROM mail_messages WHERE folder_id::text = $1 AND uid <= $2`, f.ID, maxUID); err != nil {
			return nil, false, err
		}
		lastUID, changed = 0, true
	}
	fresh := lastUID != 0 && f.Role == "inbox"
	sent := lastUID != 0 && f.Role == "sent" // a letter gone (from go24 or not) goes into its CRM cards
	max := lastUID
	// the folder's progress is kept even when the fetch breaks off, so that the next time goes on from there; a fetch
	// of the inbox running beside it (IDLE, see watch.go) does not take it back
	keep := func(err error) ([]arrived, bool, error) {
		_, uerr := s.db.Exec(context.WithoutCancel(ctx), `UPDATE mail_folders SET
			last_uid = CASE WHEN validity = $3 THEN greatest(last_uid, $2) ELSE $2 END, validity = $3,
			synced_at = CASE WHEN $4 THEN now() ELSE synced_at END WHERE id::text = $1`, f.ID, int64(max), int64(box.UidValidity), err == nil)
		if err == nil {
			err = uerr
		}
		return news, changed, err
	}
	if box.Messages > 0 {
		set := new(imap.SeqSet)
		fetch := c.Fetch
		switch {
		case lastUID == 0:
			from := uint32(1)
			if box.Messages > firstFetch {
				from = box.Messages - firstFetch + 1
			}
			set.AddRange(from, box.Messages)
		case box.UidNext != 0 && box.UidNext <= lastUID+1: // nothing new: not even a fetch
			fetch = nil
		default:
			// «n:*» brings the last letter even when it is old: the new UIDs first, then only their letters, so that
			// an old letter is not downloaded again every time (Gmail throttles a mailbox fetched like that)
			uids, err := c.UidSearch(&imap.SearchCriteria{Uid: uidsFrom(lastUID + 1)})
			if err != nil {
				return nil, false, err
			}
			for _, u := range uids {
				if u > lastUID {
					set.AddNum(u)
				}
			}
			fetch = c.UidFetch
			if set.Empty() {
				fetch = nil
			}
		}
		if fetch != nil {
			section := &imap.BodySectionName{Peek: true}
			items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchFlags, imap.FetchInternalDate, section.FetchItem()}
			msgs := make(chan *imap.Message, 16)
			done := make(chan error, 1)
			go func() { done <- fetch(set, items, msgs) }()
			var saveErr error
			for m := range msgs {
				if m.Uid <= lastUID || saveErr != nil {
					continue
				}
				n, err := s.save(ctx, a, f, m, section)
				id := n.id
				if err != nil {
					saveErr = err
					continue
				}
				if fresh && id != "" {
					news = append(news, n)
				} else if id != "" {
					changed = true
				}
				if sent && id != "" {
					s.bindCRM(ctx, a.UserID, id, false)
				}
				if m.Uid > max {
					max = m.Uid
				}
			}
			if err := <-done; err != nil {
				return keep(err)
			}
			if saveErr != nil {
				return keep(saveErr)
			}
		}
		if box.Messages <= maxReconcile {
			ch, err := s.reconcile(ctx, c, f.ID)
			if changed = changed || ch; err != nil {
				return keep(err)
			}
		}
	} else if tag, err := s.db.Exec(ctx, `DELETE FROM mail_messages WHERE folder_id::text = $1 AND uid <= $2`, f.ID, maxUID); err != nil {
		return nil, false, err
	} else {
		changed = tag.RowsAffected() > 0
	}
	return keep(nil)
}

// uidsFrom: the UIDs from n on («n:*»).
func uidsFrom(n uint32) *imap.SeqSet {
	set := new(imap.SeqSet)
	set.AddRange(n, 0)
	return set
}

// reconcile: letters read or unread elsewhere change here too; letters gone from the folder go from go24. Whether
// anything changed is returned.
func (s *Store) reconcile(ctx context.Context, c *client.Client, folderID string) (bool, error) {
	set := new(imap.SeqSet)
	set.AddRange(1, 0)
	msgs := make(chan *imap.Message, 64)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchFlags}, msgs) }()
	var uids []int64
	var seen, flagged []bool
	var answered []string
	for m := range msgs {
		r, f := flagsOf(m.Flags)
		uids, seen, flagged, answered = append(uids, int64(m.Uid)), append(seen, r), append(flagged, f), append(answered, answeredOf(m.Flags))
	}
	if err := <-done; err != nil {
		return false, err
	}
	// a flag set elsewhere is a flag here (not completed); a flag taken off elsewhere is gone (with its reminder), but a
	// completed one stays; a letter answered elsewhere is answered here (go24's own answer keeps its time)
	tag, err := s.db.Exec(ctx, `UPDATE mail_messages x SET seen = v.seen, flagged = v.flagged,
			flag_done = x.flag_done AND NOT v.flagged, flag_due = CASE WHEN v.flagged OR x.flag_done THEN x.flag_due END,
			flag_remind = CASE WHEN v.flagged THEN x.flag_remind END, flag_remind_by = CASE WHEN v.flagged THEN x.flag_remind_by END,
			answered = CASE WHEN v.answered <> '' THEN v.answered ELSE x.answered END,
			answered_at = CASE WHEN v.answered = '' OR v.answered = x.answered THEN x.answered_at END
		FROM unnest($2::bigint[], $3::boolean[], $4::boolean[], $5::text[]) AS v(uid, seen, flagged, answered)
		WHERE x.folder_id::text = $1 AND x.uid = v.uid AND (x.seen <> v.seen OR x.flagged <> v.flagged OR (v.answered <> '' AND x.answered <> v.answered))`,
		folderID, uids, seen, flagged, answered)
	if err != nil {
		return false, err
	}
	gone, err := s.db.Exec(ctx, `DELETE FROM mail_messages WHERE folder_id::text = $1 AND uid <= $3 AND NOT (uid = ANY($2))`, folderID, uids, maxUID)
	return tag.RowsAffected()+gone.RowsAffected() > 0, err
}

// save keeps a fetched letter: its id ("" — kept already) and whether it is bulk mail.
func (s *Store) save(ctx context.Context, a account, f Folder, m *imap.Message, section *imap.BodySectionName) (arrived, error) {
	var fromAddr, fromName, to, cc, bcc, subject, own string
	var refs []string
	sent := m.InternalDate
	if e := m.Envelope; e != nil {
		subject = e.Subject
		if !e.Date.IsZero() {
			sent = e.Date
		}
		if len(e.From) > 0 {
			fromAddr, fromName = e.From[0].Address(), e.From[0].PersonalName
		}
		var tos []string
		for _, t := range e.To {
			tos = append(tos, t.Address())
		}
		to = strings.Join(tos, ", ")
		var ccs []string
		for _, t := range e.Cc {
			ccs = append(ccs, t.Address())
		}
		var bccs []string // only a sent letter's own copy has them
		for _, t := range e.Bcc {
			bccs = append(bccs, t.Address())
		}
		cc, bcc, own = strings.Join(ccs, ", "), strings.Join(bccs, ", "), e.MessageId
		if e.InReplyTo != "" {
			refs = []string{e.InReplyTo}
		}
	}
	if sent.IsZero() {
		sent = time.Now()
	}
	if f.Role == "drafts" && own != "" { // go24's own draft (or one being removed) is not a letter of the server's
		if ours, err := s.ownDraft(ctx, a.ID, msgID(own)); err != nil || ours {
			return arrived{}, err
		}
	}
	var p parsed
	if r := m.GetBody(section); r != nil {
		p = parseFull(r)
		if len(p.refs) > 0 {
			refs = p.refs
		}
	}
	if f.Role == "sent" || f.Role == "drafts" || strings.EqualFold(fromAddr, a.Email) {
		p.receiptTo, p.unsub = "", "" // the user's own letter asks the others, not the user
	}
	seen, flagged := flagsOf(m.Flags)
	other, bulk := false, p.bulk || Bulk(fromAddr, nil) // «Отсортированные» or «Другие» (the inbox only)
	if f.Role == "inbox" {
		var err error
		if other, err = s.otherFor(ctx, a.UserID, fromAddr, bulk); err != nil {
			return arrived{}, err
		}
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, cc_addr, subject, body,
		sent_at, seen, flagged, message_id, thread, body_html, bcc_addr, importance, receipt_to, other, ics, answered,
		unsubscribe, unsubscribe_one_click, voting, vote)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25) ON CONFLICT DO NOTHING RETURNING id`, a.ID, f.ID, int64(m.Uid),
		clip(fromAddr, 300), clip(fromName, 300), clip(to, 2000), clip(cc, 2000), clip(subject, 1000), p.body, sent, seen, flagged, msgID(own),
		threadOf(own, refs), p.html, clip(bcc, 2000), p.importance, p.receiptTo, other, p.ics, answeredOf(m.Flags),
		p.unsub, p.oneClick, p.voting, p.vote).Scan(&id)
	files := p.files
	if errors.Is(err, pgx.ErrNoRows) { // kept already
		return arrived{}, nil
	}
	if err == nil && p.ics != "" && f.Role != "sent" && f.Role != "drafts" { // an answer, an update or a cancel of a meeting
		s.invitesArrived(ctx, a, p.ics)
	}
	if err != nil || s.Keep == nil {
		return arrived{id, bulk, p.fwd}, err
	}
	for _, file := range files {
		if err := s.Keep(ctx, a.UserID, id, file); err != nil {
			return arrived{}, err
		}
	}
	return arrived{id, bulk, p.fwd}, nil
}

// flagsOf: read, flagged.
func flagsOf(flags []string) (seen, flagged bool) {
	for _, fl := range flags {
		seen = seen || fl == imap.SeenFlag
		flagged = flagged || fl == imap.FlaggedFlag
	}
	return seen, flagged
}

// answeredOf: replied to (\Answered) or forwarded ($Forwarded) — by go24 or another mail program; "" — neither.
func answeredOf(flags []string) string {
	fwd := false
	for _, fl := range flags {
		if fl == imap.AnsweredFlag {
			return "reply"
		}
		fwd = fwd || strings.EqualFold(fl, "$Forwarded")
	}
	if fwd {
		return "fwd"
	}
	return ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// maxBody bounds the text kept of a letter.
const maxBody = 200 << 10

var reBlank = regexp.MustCompile(`\n\s*\n\s*\n+`)

// Attachment is a file of a letter; ContentID — a picture of its text (cid:), not shown as an attachment.
type Attachment struct {
	Name, ContentType string
	Data              []byte
	ContentID         string
}

// maxAttachment bounds one file of a letter go24 keeps (bigger ones stay in the mailbox only).
const maxAttachment = 25 << 20

// parse is the letter's plain text — its text/plain part, or its HTML's text — and its attachments.
func parse(r io.Reader) (string, []Attachment) {
	p := parseFull(r)
	return p.body, p.files
}

// parsed is what go24 keeps of a letter's source.
type parsed struct {
	body, html string // the text; the HTML, cleaned ("" — none)
	files      []Attachment
	refs       []string // References, then In-Reply-To: the letters this one answers, the conversation's root first
	importance int      // -1 low, 0 normal, 1 high
	receiptTo  string   // the address a read receipt is asked to
	unsub      string   // List-Unsubscribe: how to leave the newsletter (see unsubscribe.go)
	oneClick   bool     // List-Unsubscribe-Post offers leaving it in one click (RFC 8058)
	bulk       bool     // sent to many or by a robot (see Bulk)
	fwd        bool     // sent on by a mail program itself (see arrived)
	ics        string   // a meeting it carries: its text/calendar part, else a calendar file (see ics.go)
	voting     string   // the voting buttons it asks for, the choice it votes for (voting.go)
	vote       string
}

// parseFull: the text and HTML, the attachments and pictures, the letters it answers, its importance.
func parseFull(r io.Reader) (out parsed) {
	mr, err := gomail.CreateReader(r)
	if err != nil {
		return out
	}
	out.refs, _ = mr.Header.MsgIDList("References")
	if irt, _ := mr.Header.MsgIDList("In-Reply-To"); len(irt) > 0 {
		out.refs = append(out.refs, irt[0])
	}
	out.bulk = Bulk("", mr.Header.Get)
	out.fwd = strings.EqualFold(strings.TrimSpace(mr.Header.Get("Auto-Submitted")), "auto-forwarded") || mr.Header.Get("Resent-From") != ""
	out.unsub = clip(strings.TrimSpace(mr.Header.Get("List-Unsubscribe")), 2000)
	out.oneClick = strings.Contains(strings.ToLower(mr.Header.Get("List-Unsubscribe-Post")), "one-click")
	out.voting, out.vote = votingOf(mr.Header.Text)
	out.importance = importanceOf(mr.Header.Get("Importance"), mr.Header.Get("X-Priority"), mr.Header.Get("Priority"))
	if to := mr.Header.Get("Disposition-Notification-To"); to != "" {
		if a, err := mail.ParseAddress(to); err == nil {
			out.receiptTo = clip(a.Address, 300)
		}
	}
	var plain, htm string
	var files []Attachment
	add := func(name, ct, cid string, body io.Reader) {
		b, _ := io.ReadAll(io.LimitReader(body, maxAttachment+1))
		if (name == "" && cid == "") || len(b) > maxAttachment || len(files) >= 40 {
			return
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		if name == "" {
			name = "image"
		}
		files = append(files, Attachment{Name: clip(name, 200), ContentType: ct, Data: b, ContentID: clip(cid, 300)})
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		switch h := p.Header.(type) {
		case *gomail.InlineHeader:
			ct, params, _ := h.ContentType()
			switch {
			case ct == "text/plain" && plain == "":
				b, _ := io.ReadAll(io.LimitReader(p.Body, maxBody))
				plain = string(b)
			case ct == "text/html" && htm == "":
				b, _ := io.ReadAll(io.LimitReader(p.Body, maxHTML))
				htm = string(b)
			case ct == "text/calendar" && out.ics == "":
				b, _ := io.ReadAll(io.LimitReader(p.Body, maxICS))
				out.ics = string(b)
			case !strings.HasPrefix(ct, "text/"): // a picture of the text, or a file shown inline
				cid := strings.Trim(h.Get("Content-Id"), "<> ")
				_, dp, _ := h.ContentDisposition()
				name := dp["filename"]
				if name == "" {
					name = params["name"]
				}
				if !strings.HasPrefix(ct, "image/") {
					cid = ""
				}
				add(name, ct, cid, p.Body)
			}
		case *gomail.AttachmentHeader:
			name, _ := h.Filename()
			ct, _, _ := h.ContentType()
			cid := ""
			if c := strings.Trim(h.Get("Content-Id"), "<> "); c != "" && strings.HasPrefix(ct, "image/") && strings.Contains(htm, "cid:"+c) {
				cid = c // a picture of the text sent as an attachment
			}
			if out.ics == "" && (ct == "text/calendar" || ct == "application/ics" || strings.HasSuffix(strings.ToLower(name), ".ics")) {
				b, _ := io.ReadAll(io.LimitReader(p.Body, maxICS))
				out.ics = string(b)
				add(name, ct, cid, strings.NewReader(out.ics))
				continue
			}
			add(name, ct, cid, p.Body)
		}
	}
	if htm != "" {
		out.html = Sanitize(htm)
		if plain == "" {
			plain = HTMLText(out.html)
		}
	}
	out.body, out.files = clip(strings.TrimSpace(reBlank.ReplaceAllString(strings.ReplaceAll(plain, "\r\n", "\n"), "\n\n")), maxBody), files
	return out
}

// importanceOf reads Importance (Outlook), X-Priority (1–2 high, 4–5 low) and Priority (RFC 2156).
func importanceOf(importance, xPriority, priority string) int {
	switch strings.ToLower(strings.TrimSpace(importance)) {
	case "high":
		return 1
	case "low":
		return -1
	}
	if f := strings.Fields(xPriority); len(f) > 0 {
		switch f[0] {
		case "1", "2":
			return 1
		case "4", "5":
			return -1
		}
	}
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "urgent":
		return 1
	case "non-urgent":
		return -1
	}
	return 0
}

// msgID is a Message-ID as go24 keeps it: without the angle brackets, lower case.
func msgID(s string) string {
	return clip(strings.ToLower(strings.Trim(strings.TrimSpace(s), "<>")), 500)
}

// threadOf: the conversation of a letter is its root's Message-ID — the first of References, else In-Reply-To,
// else its own (Outlook and most clients thread the same way).
func threadOf(own string, refs []string) string {
	for _, r := range refs {
		if r = msgID(r); r != "" {
			return r
		}
	}
	return msgID(own)
}

// text is the letter's plain text.
func text(r io.Reader) string {
	t, _ := parse(r)
	return t
}

// SyncDue fetches the mailboxes not fetched for a while (the worker calls it).
func (s *Store) SyncDue(ctx context.Context, every time.Duration) (errs []error) {
	rows, err := s.db.Query(ctx, `SELECT id::text FROM mailboxes WHERE synced_at IS NULL OR synced_at < now() - $1::interval ORDER BY synced_at NULLS FIRST LIMIT 20`,
		strconv.Itoa(int(every.Seconds()))+" seconds")
	if err != nil {
		return []error{err}
	}
	ids, err := pgxCollectStrings(rows)
	if err != nil {
		return []error{err}
	}
	for _, id := range ids {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute) // a throttled server (Gmail) answers each command in seconds
		if err := s.Sync(cctx, id); err != nil {
			errs = append(errs, fmt.Errorf("mailbox %s: %w", id, err))
		}
		cancel()
	}
	return errs
}
