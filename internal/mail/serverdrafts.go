package mail

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/textproto"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// Drafts on the server, as Outlook keeps them: a go24 draft is put into the server's «Черновики» (\Draft) at the
// mailbox's sync, the copy before it removed — so the letter being written is there on the phone and in other
// programs; a draft sent, deleted or moved to another mailbox has its copy removed at the next sync
// (mail_draft_drops). The copy carries the draft's own Message-ID, by which it is found and is not fetched back.

// pushDrafts puts the mailbox's changed drafts on the server and removes the copies no longer wanted.
func (s *Store) pushDrafts(ctx context.Context, c *client.Client, a account) error {
	f, err := s.folderByRole(ctx, a.ID, "drafts")
	if err != nil || f.ID == "" || f.Local {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT message_id FROM mail_draft_drops WHERE mailbox_id::text = $1`, a.ID)
	if err != nil {
		return err
	}
	drops, err := pgxCollectStrings(rows)
	if err != nil {
		return err
	}
	type due struct {
		id, user, own, box, old string
		at                      time.Time
	}
	var list []due
	// not one waiting the seconds of «Отменить» (it is about to go); a scheduled one stays in «Черновики» till its time
	rows, err = s.db.Query(ctx, `SELECT d.id::text, d.user_id::text, x.message_id, coalesce(d.server_box::text, ''), d.server_id, d.updated_at
		FROM mail_drafts d JOIN mail_messages x ON x.id = d.id
		WHERE x.mailbox_id::text = $1 AND NOT d.sending AND (d.send_at IS NULL OR d.scheduled)
			AND (d.pushed_at IS NULL OR d.pushed_at < d.updated_at OR d.server_box IS DISTINCT FROM x.mailbox_id)
		ORDER BY d.updated_at`, a.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.user, &d.own, &d.box, &d.old, &d.at); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(drops) == 0 && len(list) == 0 {
		return nil
	}
	if _, err := c.Select(f.Name, false); err != nil {
		return err
	}
	gone := append([]string{}, drops...)
	for _, d := range list {
		if d.box == a.ID && d.old != "" {
			gone = append(gone, d.old)
		}
	}
	if err := dropCopies(c, gone); err != nil {
		return err
	}
	if len(drops) > 0 {
		if _, err := s.db.Exec(ctx, `DELETE FROM mail_draft_drops WHERE mailbox_id::text = $1 AND message_id = ANY($2)`, a.ID, drops); err != nil {
			return err
		}
	}
	for _, d := range list {
		if d.box != "" && d.box != a.ID && d.old != "" { // the draft went to another mailbox: that one's copy goes at its sync
			if _, err := s.db.Exec(ctx, `INSERT INTO mail_draft_drops (mailbox_id, message_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
				d.box, d.old); err != nil {
				return err
			}
		}
		_, l, err := s.draftLetter(ctx, d.user, d.id)
		if err != nil {
			return err
		}
		if err := c.Append(f.Name, []string{imap.SeenFlag, imap.DraftFlag}, time.Now(), bytes.NewReader(s.draftMIME(ctx, a, d.user, d.own, l))); err != nil {
			return err
		}
		if _, err := s.db.Exec(ctx, `UPDATE mail_drafts SET pushed_at = $2, server_box = $3, server_id = $4 WHERE id::text = $1`,
			d.id, d.at, a.ID, d.own); err != nil {
			return err
		}
	}
	return nil
}

// dropCopies removes from the selected folder the letters of these Message-IDs.
func dropCopies(c *client.Client, ids []string) error {
	set := new(imap.SeqSet)
	for _, id := range ids {
		uids, err := c.UidSearch(&imap.SearchCriteria{Header: textproto.MIMEHeader{"Message-Id": {"<" + id + ">"}}})
		if err != nil {
			return err
		}
		set.AddNum(uids...)
	}
	if set.Empty() {
		return nil
	}
	if err := c.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil); err != nil {
		return err
	}
	return c.Expunge(nil)
}

// draftMIME is the draft as a letter of the server's «Черновики»: whom it is to (blind copies too), from the address chosen.
func (s *Store) draftMIME(ctx context.Context, a account, userID, own string, l Letter) []byte {
	var e envelope
	e.To, _ = addresses(l.To) // what is not an address yet is not kept on the server
	e.CC, _ = addresses(l.CC)
	e.BCC, _ = addresses(l.BCC)
	var err error
	if e.From, err = s.sender(ctx, userID, a.ID, l.From); err != nil {
		e.From.Address = a.Email
	}
	e.Host, e.Now, e.ID, e.WithBCC = a.Email[strings.LastIndex(a.Email, "@")+1:], time.Now(), own, true
	if l.HTML != "" {
		l.Text = HTMLText(l.HTML)
	}
	msg, _ := compose(e, l)
	return msg
}

// ownDraft: is the letter of this Message-ID go24's draft put on the server, or a copy being removed?
func (s *Store) ownDraft(ctx context.Context, mailboxID, own string) (bool, error) {
	var ours bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_drafts d JOIN mail_messages x ON x.id = d.id
			WHERE x.message_id = $2 AND d.server_box::text = $1)
		OR EXISTS (SELECT 1 FROM mail_draft_drops WHERE mailbox_id::text = $1 AND message_id = $2)`, mailboxID, own).Scan(&ours)
	return ours, err
}

// AdoptDraft makes a draft written in another program (a letter of the server's «Черновики») go24's own, so that it
// opens in the editor; its server copy is replaced by go24's at the next sync. A letter that is no such draft — false.
func (s *Store) AdoptDraft(ctx context.Context, userID, id string) (bool, error) {
	m, err := s.message(ctx, userID, id)
	if err != nil || m.Draft || m.FolderRole != "drafts" {
		return false, err
	}
	text := m.HTML
	if text == "" {
		text = "<p>" + strings.ReplaceAll(html.EscapeString(m.Body), "\n", "<br>") + "</p>"
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// go24's own uid: the letter is no longer the server's, whose copy (found by the Message-ID) goes at the push
	tag, err := tx.Exec(ctx, `UPDATE mail_messages SET uid = $2, body_html = $3, seen = true WHERE id::text = $1 AND uid <= $4`,
		id, time.Now().UnixNano(), Sanitize(text), maxUID)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	var own string
	if err := tx.QueryRow(ctx, `SELECT message_id FROM mail_messages WHERE id::text = $1`, id).Scan(&own); err != nil {
		return false, err
	}
	if own == "" { // a Message-ID of its own, by which go24's copy is known
		own = msgID(fmt.Sprintf("%d.go24@%s", time.Now().UnixNano(), m.MailboxEmail[strings.LastIndex(m.MailboxEmail, "@")+1:]))
		if _, err := tx.Exec(ctx, `UPDATE mail_messages SET message_id = $2 WHERE id::text = $1`, id, own); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mail_drafts (id, user_id, server_box, server_id) VALUES ($1, $2, $3, $4)`,
		id, userID, m.MailboxID, own); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
