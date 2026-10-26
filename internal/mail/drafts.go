package mail

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Drafts, as in Outlook: a letter being written is kept by itself in «Черновики» a few seconds after each change,
// with its files (uploaded at once); it opens again from the folder. «Отправить позже» keeps it there till its
// time, and after «Отправить» it waits the seconds «Отменить» is offered (send_at) — then a sender takes it
// (SendDue: the worker, and the web server right after the delay), and it leaves «Черновики» for «Отправленные».

// Draft is a letter being written.
type Draft struct {
	ID, MailboxID              string
	From                       string // the address it goes from (the mailbox's own, or one of its aliases)
	To, CC, BCC, Subject, HTML string
	Importance                 int
	Voting, Vote               string // the voting buttons it asks for («A;B»); the choice an answer votes for (voting.go)
	Receipt, Delivery          bool
	ReplyTo, Mode              string // the letter answered (reply, all), forwarded (fwd; fwdatt — as an attachment) or sent again (resend)
	SendAt                     *time.Time
	Scheduled                  bool // SendAt is the user's time («Отправить позже»), not the undo delay
	Error                      string
	UpdatedAt                  time.Time
}

// Modes of a draft: a new letter, an answer to the sender, to everyone, a forward, a forward as an attachment, a
// sent letter sent again («Отправить повторно»).
var Modes = []string{"", "reply", "all", "fwd", "fwdatt", "resend"}

func checkDraft(d *Draft) error {
	ok := false
	for _, m := range Modes {
		ok = ok || d.Mode == m
	}
	if !ok || d.Importance < -1 || d.Importance > 1 {
		return i18n.E("err.bad_form")
	}
	d.To, d.CC, d.BCC = clip(strings.TrimSpace(d.To), 4000), clip(strings.TrimSpace(d.CC), 4000), clip(strings.TrimSpace(d.BCC), 4000)
	d.Subject = clip(strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(d.Subject)), 1000)
	d.HTML = Sanitize(d.HTML)
	d.Voting, d.Vote = Choices(d.Voting), clip(strings.TrimSpace(d.Vote), maxChoice)
	return nil
}

// Check: can the draft go — someone to send it to, the addresses right?
func (d Draft) Check() error {
	n := 0
	for _, list := range []string{d.To, d.CC, d.BCC} {
		a, err := addresses(list)
		if err != nil {
			return i18n.E("err.mail_to")
		}
		n += len(a)
	}
	if n == 0 || n > MaxRcpts {
		return i18n.E("err.mail_to")
	}
	return nil
}

// draftFolder is the mailbox's «Черновики» — go24's own when the server has none.
func (s *Store) draftFolder(ctx context.Context, mailboxID string) (string, error) {
	f, err := s.folderByRole(ctx, mailboxID, "drafts")
	if err != nil || f.ID != "" {
		return f.ID, err
	}
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'Drafts', 'drafts', true)
		ON CONFLICT (mailbox_id, name) DO UPDATE SET role = 'drafts' RETURNING id::text`, mailboxID).Scan(&id)
	return id, err
}

// SaveDraft keeps a letter being written (a new draft when d.ID is ""), and returns its id. A draft being sent
// is not changed.
func (s *Store) SaveDraft(ctx context.Context, userID string, d Draft) (string, error) {
	if err := checkDraft(&d); err != nil {
		return "", err
	}
	box, err := s.Mailbox(ctx, userID, d.MailboxID)
	if err != nil {
		return "", err
	}
	from, err := s.sender(ctx, userID, box.ID, d.From)
	if err != nil {
		return "", err
	}
	folder, err := s.draftFolder(ctx, box.ID)
	if err != nil {
		return "", err
	}
	text := HTMLText(d.HTML)
	if d.ID != "" {
		tag, err := s.db.Exec(ctx, `UPDATE mail_messages x SET mailbox_id = $3, folder_id = $4, from_addr = $5, to_addr = $6, cc_addr = $7,
			bcc_addr = $8, subject = $9, body = $10, body_html = $11, importance = $12, voting = $13, vote = $14, sent_at = now()
			FROM mail_drafts d WHERE d.id = x.id AND x.id::text = $1 AND d.user_id::text = $2 AND NOT d.sending`,
			d.ID, userID, box.ID, folder, from.Address, d.To, d.CC, d.BCC, d.Subject, text, d.HTML, d.Importance, d.Voting, d.Vote)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() == 0 {
			return "", ErrNotFound
		}
		_, err = s.db.Exec(ctx, `UPDATE mail_drafts SET receipt = $2, delivery = $3, updated_at = now() WHERE id::text = $1`, d.ID, d.Receipt, d.Delivery)
		return d.ID, err
	}
	var refs []string
	if d.Mode != "resend" { // a letter sent again is a new one, not an answer
		if refs, err = s.refsOf(ctx, userID, d.ReplyTo); err != nil {
			return "", err
		}
	}
	own := msgID(fmt.Sprintf("%d.go24@%s", time.Now().UnixNano(), box.Email[strings.LastIndex(box.Email, "@")+1:]))
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, to_addr, cc_addr, bcc_addr, subject, body,
		body_html, importance, sent_at, seen, message_id, thread, voting, vote) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now(), true, $12, $13, $14, $15)
		RETURNING id::text`, box.ID, folder, time.Now().UnixNano(), from.Address, d.To, d.CC, d.BCC, d.Subject, text, d.HTML, d.Importance,
		own, threadOf(own, refs), d.Voting, d.Vote).Scan(&id); err != nil {
		return "", err
	}
	if refs == nil {
		refs = []string{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mail_drafts (id, user_id, reply_to, mode, refs, receipt, delivery) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, userID, d.ReplyTo, d.Mode, refs, d.Receipt, d.Delivery); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if (d.Mode == "fwd" || d.Mode == "resend") && d.ReplyTo != "" && s.Keep != nil && s.Open != nil { // a forward takes the letter's files along
		rows, err := s.db.Query(ctx, `SELECT f.id::text FROM files f WHERE f.mail_id::text = $1 AND f.content_id = '' ORDER BY f.name`, d.ReplyTo)
		if err != nil {
			return id, err
		}
		list, err := pgxCollectStrings(rows)
		if err != nil {
			return id, err
		}
		for _, f := range list {
			a, err := s.readFile(ctx, userID, f)
			if err == nil {
				err = s.Keep(ctx, userID, id, a)
			}
			if err != nil {
				return id, err
			}
		}
	}
	if d.Mode == "fwdatt" && d.ReplyTo != "" && s.Keep != nil { // the letter itself goes as «Тема.eml»
		m, err := s.message(ctx, userID, d.ReplyTo)
		if err != nil {
			return id, err
		}
		files, err := s.letterFiles(ctx, userID, d.ReplyTo)
		if err != nil {
			return id, err
		}
		if err := s.Keep(ctx, userID, id, letterAttachment(m, files)); err != nil {
			return id, err
		}
	}
	return id, nil
}

const draftSQL = `SELECT x.id::text, x.mailbox_id::text, x.from_addr, x.to_addr, x.cc_addr, x.bcc_addr, x.subject, x.body_html, x.importance::int,
		x.voting, x.vote, d.receipt, d.delivery, d.reply_to, d.mode, d.send_at, d.scheduled, d.error, d.updated_at
	FROM mail_drafts d JOIN mail_messages x ON x.id = d.id`

// Draft is a draft of the user.
func (s *Store) Draft(ctx context.Context, userID, id string) (Draft, error) {
	rows, err := s.db.Query(ctx, draftSQL+` WHERE d.id::text = $1 AND d.user_id::text = $2`, id, userID)
	if err != nil {
		return Draft{}, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Draft])
	if err != nil {
		return Draft{}, err
	}
	if len(list) == 0 {
		return Draft{}, ErrNotFound
	}
	return list[0], nil
}

// DeleteDraft throws a draft away with its files («Отменить» of a new letter, «Удалить черновик»).
func (s *Store) DeleteDraft(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_messages x USING mail_drafts d WHERE d.id = x.id AND x.id::text = $1 AND d.user_id::text = $2
		AND NOT d.sending`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

func newCID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + "@go24"
}

// AttachDraft keeps a file with a draft — an attachment, or a picture of its text (inline) — and returns it.
func (s *Store) AttachDraft(ctx context.Context, userID, draftID string, f Attachment, inline bool) (File, error) {
	if s.Keep == nil {
		return File{}, errors.New("mail: no file storage")
	}
	var n int
	var size int64
	if err := s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE fl.content_id = ''), coalesce(sum(fl.size), 0)::bigint
		FROM mail_drafts d LEFT JOIN files fl ON fl.mail_id = d.id WHERE d.id::text = $1 AND d.user_id::text = $2 AND NOT d.sending
		GROUP BY d.id`, draftID, userID).Scan(&n, &size); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return File{}, ErrNotFound
		}
		return File{}, err
	}
	if (!inline && n >= MaxSendFiles) || size+int64(len(f.Data)) > MaxSendSize {
		return File{}, i18n.E("err.mail_files", MaxSendFiles)
	}
	if inline {
		if !strings.HasPrefix(f.ContentType, "image/") || f.ContentType == "image/svg+xml" {
			return File{}, i18n.E("err.mail_picture")
		}
		f.ContentID = newCID()
	}
	if err := s.Keep(ctx, userID, draftID, f); err != nil {
		return File{}, err
	}
	var out File
	err := s.db.QueryRow(ctx, `SELECT id::text, name, content_type, size FROM files WHERE mail_id::text = $1 ORDER BY created_at DESC, id LIMIT 1`,
		draftID).Scan(&out.ID, &out.Name, &out.ContentType, &out.Size)
	return out, err
}

// DropDraftFile takes a file off a draft.
func (s *Store) DropDraftFile(ctx context.Context, userID, draftID, fileID string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM files fl USING mail_drafts d WHERE fl.mail_id = d.id AND d.id::text = $1 AND d.user_id::text = $2
		AND fl.id::text = $3 AND NOT d.sending`, draftID, userID, fileID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// ScheduleDraft has a draft sent at a time: the user's («Отправить позже»: scheduled), or now plus the seconds
// «Отменить» is offered.
func (s *Store) ScheduleDraft(ctx context.Context, userID, id string, at time.Time, scheduled bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE mail_drafts SET send_at = $3, scheduled = $4, error = '', updated_at = now()
		WHERE id::text = $1 AND user_id::text = $2 AND NOT sending`, id, userID, at, scheduled)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// CancelSend keeps a draft from going («Отменить» after «Отправить», or a scheduled one): it stays a draft. A
// letter already being sent cannot be called back.
func (s *Store) CancelSend(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE mail_drafts SET send_at = NULL, scheduled = false, updated_at = now()
		WHERE id::text = $1 AND user_id::text = $2 AND NOT sending`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		var ok bool
		_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_drafts WHERE id::text = $1 AND user_id::text = $2)`, id, userID).Scan(&ok)
		if ok {
			return i18n.E("err.mail_sending")
		}
		return i18n.E("err.mail_gone")
	}
	return err
}

// SendDraft sends a draft of the user now.
func (s *Store) SendDraft(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE mail_drafts SET sending = true, updated_at = now() WHERE id::text = $1 AND user_id::text = $2 AND NOT sending`,
		id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return s.sendTaken(ctx, userID, id)
}

// SendDue sends the drafts whose time has come (the worker calls it; the web server too, right after the undo delay).
func (s *Store) SendDue(ctx context.Context) (errs []error) {
	for i := 0; i < 50; i++ {
		var id, user string
		err := s.db.QueryRow(ctx, `UPDATE mail_drafts SET sending = true, updated_at = now() WHERE id = (SELECT id FROM mail_drafts
			WHERE send_at <= now() AND NOT sending ORDER BY send_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id::text, user_id::text`).Scan(&id, &user)
		if errors.Is(err, pgx.ErrNoRows) {
			return errs
		}
		if err != nil {
			return append(errs, err)
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := s.sendTaken(cctx, user, id); err != nil {
			errs = append(errs, fmt.Errorf("draft %s: %w", id, err))
		}
		cancel()
	}
	return errs
}

var reDraftPic = regexp.MustCompile(`src="/files/([0-9a-fA-F-]{36})(?:\?inline=1)?"`)

var reDataPic = regexp.MustCompile(`src="data:image/(png|gif|jpeg|webp);base64,([^"]+)"`)

// draftLetter is the letter a draft makes: its files, the pictures of its text as cid: parts, the user's font.
func (s *Store) draftLetter(ctx context.Context, userID, id string) (d Draft, l Letter, _ error) {
	d, err := s.Draft(ctx, userID, id)
	if err != nil {
		return d, l, err
	}
	var refs []string
	if err := s.db.QueryRow(ctx, `SELECT refs FROM mail_drafts WHERE id::text = $1`, id).Scan(&refs); err != nil {
		return d, l, err
	}
	l = Letter{From: d.From, To: d.To, CC: d.CC, BCC: d.BCC, Subject: d.Subject, HTML: d.HTML, Importance: d.Importance, Receipt: d.Receipt,
		Delivery: d.Delivery, Refs: refs, Voting: d.Voting, Vote: d.Vote}
	if d.Voting != "" { // the choices in words too, for those who see no buttons
		l.HTML += s.votingText(d.Voting)
	}
	rows, err := s.db.Query(ctx, `SELECT id::text FROM files WHERE mail_id::text = $1 AND content_id = '' ORDER BY created_at, id`, id)
	if err != nil {
		return d, l, err
	}
	attached, err := pgxCollectStrings(rows)
	if err != nil {
		return d, l, err
	}
	for _, f := range attached {
		a, err := s.readFile(ctx, userID, f)
		if err != nil {
			return d, l, err
		}
		l.Files = append(l.Files, a)
	}
	// pictures of the text: go24's files (the draft's own, or of the letter quoted) go along as cid: parts
	cids := map[string]string{}
	var picErr error
	l.HTML = reDraftPic.ReplaceAllStringFunc(l.HTML, func(m string) string {
		fid := reDraftPic.FindStringSubmatch(m)[1]
		if cid, ok := cids[fid]; ok {
			return `src="cid:` + cid + `"`
		}
		a, err := s.readFile(ctx, userID, fid)
		if errors.Is(err, ErrNotFound) {
			return `src=""`
		}
		if err != nil {
			picErr = err
			return m
		}
		if a.ContentID == "" {
			a.ContentID = newCID()
		}
		cids[fid] = a.ContentID
		l.Files = append(l.Files, a)
		return `src="cid:` + a.ContentID + `"`
	})
	if picErr != nil {
		return d, l, picErr
	}
	l.HTML = reDataPic.ReplaceAllStringFunc(l.HTML, func(m string) string { // pictures kept in the text (a signature's)
		sub := reDataPic.FindStringSubmatch(m)
		data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(sub[2]), ""))
		if err != nil {
			return `src=""`
		}
		a := Attachment{Name: "image." + strings.Replace(sub[1], "jpeg", "jpg", 1), ContentType: "image/" + sub[1], Data: data, ContentID: newCID()}
		l.Files = append(l.Files, a)
		return `src="cid:` + a.ContentID + `"`
	})
	if p, err := s.Prefs(ctx, userID); err == nil && (p.Font != "" || p.FontSize != "") { // the letter's font, as the editor showed it
		var css []string
		if p.Font != "" {
			css = append(css, "font-family: "+p.Font)
		}
		if p.FontSize != "" {
			css = append(css, "font-size: "+p.FontSize)
		}
		l.HTML = `<div style="` + strings.Join(css, "; ") + `">` + l.HTML + `</div>`
	}
	return d, l, nil
}

// sendTaken sends a draft taken for sending: on success it is gone from «Черновики» (the answered letter is marked
// answered on the server), on failure it stays with the reason.
func (s *Store) sendTaken(ctx context.Context, userID, id string) error {
	err := func() error {
		d, l, err := s.draftLetter(ctx, userID, id)
		if err != nil {
			return err
		}
		if err := s.SendLetter(ctx, userID, d.MailboxID, l); err != nil {
			return err
		}
		if d.ReplyTo != "" && d.Mode != "resend" {
			flag, mode := imap.AnsweredFlag, "reply"
			if d.Mode == "fwd" || d.Mode == "fwdatt" {
				flag, mode = "$Forwarded", "fwd"
			}
			if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET answered = $2, answered_at = now() WHERE id::text = $1`, d.ReplyTo, mode); err != nil && s.Log != nil {
				s.Log("mail: mark answered", d.ReplyTo, err)
			}
			if list, err := s.letters(ctx, userID, []string{d.ReplyTo}); err == nil && len(list) > 0 {
				if err := s.store(ctx, list, flag, true); err != nil && s.Log != nil {
					s.Log("mail: mark answered", d.ReplyTo, err)
				}
			}
		}
		_, err = s.db.Exec(ctx, `DELETE FROM mail_messages WHERE id::text = $1`, id)
		return err
	}()
	if err != nil {
		msg := err.Error()
		var ie *i18n.Error
		if errors.As(err, &ie) {
			msg = ie.Key
		}
		if _, uerr := s.db.Exec(ctx, `UPDATE mail_drafts SET sending = false, send_at = NULL, scheduled = false, error = left($2, 500)
			WHERE id::text = $1`, id, msg); uerr != nil && s.Log != nil {
			s.Log("mail: keep the draft's error", id, uerr)
		}
	}
	return err
}
