package mail

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Восстановить удалённые элементы», as in Outlook: a letter deleted for good from «Удалённые» or «Нежелательная
// почта» (one by one or «Очистить папку») is kept by go24 — its source brought from the server just before EXPUNGE —
// for RecoverDays; «Восстановить» puts it back on the server to the folder it was deleted from, «Удалить навсегда»
// forgets it. go24's own letters (no source on the server) and those over MaxRecoverSize are not kept.

// RecoverDays is how long go24 keeps a letter deleted for good (Outlook's default for «Элементы для восстановления»).
const RecoverDays = 30

// MaxRecoverSize bounds a letter go24 keeps after deleting it for good.
const MaxRecoverSize = 25 << 20

// recoverRoles are the folders whose letters, deleted there, are deleted for good — and kept.
var recoverRoles = map[string]bool{"trash": true, "junk": true}

// Recoverable is a letter deleted for good that can be put back.
type Recoverable struct {
	ID, MailboxID, Folder, Role string
	FromAddr, FromName, Subject string
	SentAt, DeletedAt           time.Time
	Size                        int64
}

// Until is the last moment the letter can be put back.
func (r Recoverable) Until() time.Time { return r.DeletedAt.AddDate(0, 0, RecoverDays) }

// keepGone fetches the sources of the letters of the folder now selected on c (the uids) and keeps them for
// «Восстановить»; it returns the rows made, to be dropped when the letters stay after all.
func (s *Store) keepGone(ctx context.Context, c *client.Client, folder []letter) ([]string, error) {
	if !recoverRoles[folder[0].role] {
		return nil, nil
	}
	type meta struct {
		from, name, subject string
		sent                time.Time
		seen, flagged       bool
	}
	byUID := map[uint32]meta{}
	set := new(imap.SeqSet)
	rows, err := s.db.Query(ctx, `SELECT uid, from_addr, from_name, subject, sent_at, seen, flagged FROM mail_messages WHERE id::text = ANY($1)`, ids(folder))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var uid int64
		var m meta
		if err := rows.Scan(&uid, &m.from, &m.name, &m.subject, &m.sent, &m.seen, &m.flagged); err != nil {
			rows.Close()
			return nil, err
		}
		byUID[uint32(uid)] = m
		set.AddNum(uint32(uid))
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(byUID) == 0 {
		return nil, err
	}
	section := &imap.BodySectionName{Peek: true}
	msgs := make(chan *imap.Message, 4)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchRFC822Size, section.FetchItem()}, msgs)
	}()
	var made []string
	var keepErr error
	for msg := range msgs {
		m, ok := byUID[msg.Uid]
		r := msg.GetBody(section)
		if !ok || r == nil || keepErr != nil || msg.Size > MaxRecoverSize {
			continue // drained all the same: the fetch goes on till its end
		}
		src, err := io.ReadAll(io.LimitReader(r, MaxRecoverSize+1))
		if err != nil || len(src) > MaxRecoverSize {
			continue
		}
		var id string
		keepErr = s.db.QueryRow(ctx, `INSERT INTO mail_recoverable (mailbox_id, folder, role, from_addr, from_name, subject, sent_at, seen, flagged, source)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`,
			folder[0].mailbox, folder[0].name, folder[0].role, m.from, m.name, m.subject, m.sent, m.seen, m.flagged, src).Scan(&id)
		if keepErr == nil {
			made = append(made, id)
		}
	}
	if err := <-done; err != nil {
		return made, err
	}
	return made, keepErr
}

// dropKept forgets the rows keepGone made (the letters were not deleted after all).
func (s *Store) dropKept(made []string) {
	if len(made) > 0 {
		_, _ = s.db.Exec(context.Background(), `DELETE FROM mail_recoverable WHERE id::text = ANY($1)`, made)
	}
}

const recoverableSQL = `SELECT r.id::text, r.mailbox_id::text, r.folder, r.role, r.from_addr, r.from_name, r.subject, r.sent_at, r.deleted_at,
	length(r.source) FROM mail_recoverable r JOIN mailboxes m ON m.id = r.mailbox_id`

// RecoverableOf are the letters of the user's mailbox deleted for good in the last RecoverDays, the last deleted
// first; q — a part of the sender or the subject.
func (s *Store) RecoverableOf(ctx context.Context, userID, mailboxID, q string) ([]Recoverable, error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	rows, err := s.db.Query(ctx, recoverableSQL+` WHERE m.user_id::text = $1 AND m.id::text = $2
		AND r.deleted_at > now() - make_interval(days => $3)
		AND (r.subject ILIKE $4 OR r.from_addr ILIKE $4 OR r.from_name ILIKE $4)
		ORDER BY r.deleted_at DESC, r.sent_at DESC LIMIT 500`, userID, mailboxID, RecoverDays, like)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Recoverable])
}

// recoverables are the user's kept letters of the ids, with their sources, by mailbox.
func (s *Store) recoverables(ctx context.Context, userID string, ids []string) (map[string][]recovered, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id::text, r.mailbox_id::text, r.folder, r.role, r.sent_at, r.seen, r.flagged, r.source
		FROM mail_recoverable r JOIN mailboxes m ON m.id = r.mailbox_id
		WHERE m.user_id::text = $1 AND r.id::text = ANY($2) AND r.deleted_at > now() - make_interval(days => $3)
		ORDER BY r.mailbox_id, r.folder, r.sent_at`, userID, ids, RecoverDays)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[recovered])
	if err != nil {
		return nil, err
	}
	out := map[string][]recovered{}
	for _, r := range list {
		out[r.MailboxID] = append(out[r.MailboxID], r)
	}
	return out, nil
}

type recovered struct {
	ID, MailboxID, Folder, Role string
	SentAt                      time.Time
	Seen, Flagged               bool
	Source                      []byte
}

// Recover puts the kept letters back on their servers, each to the folder it was deleted from (that gone — to the
// mailbox's folder of its role, else to «Входящие»), and forgets them; it returns how many came back.
func (s *Store) Recover(ctx context.Context, userID string, ids []string) (int, error) {
	boxes, err := s.recoverables(ctx, userID, ids)
	if err != nil {
		return 0, err
	}
	if len(boxes) == 0 {
		return 0, ErrNotFound
	}
	n := 0
	for box, list := range boxes {
		err := s.remote(ctx, box, func(c *client.Client, a account) error {
			touched := map[string]Folder{}
			for _, r := range list {
				to, err := s.recoverTo(ctx, userID, r)
				if err != nil {
					return err
				}
				flags := []string{}
				if r.Seen {
					flags = append(flags, imap.SeenFlag)
				}
				if r.Flagged {
					flags = append(flags, imap.FlaggedFlag)
				}
				if err := c.Append(to.Name, flags, r.SentAt, bytes.NewReader(r.Source)); err != nil {
					return serverErr(err)
				}
				if _, err := s.db.Exec(ctx, `DELETE FROM mail_recoverable WHERE id::text = $1`, r.ID); err != nil {
					return err
				}
				n++
				touched[to.ID] = to
			}
			for _, f := range touched {
				if err := s.syncFolder(ctx, c, a, f); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// recoverTo is the folder a kept letter goes back to.
func (s *Store) recoverTo(ctx context.Context, userID string, r recovered) (Folder, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM mail_folders WHERE mailbox_id::text = $1 AND name = $2 AND selectable AND NOT local`,
		r.MailboxID, r.Folder).Scan(&id)
	if err == pgx.ErrNoRows {
		for _, role := range []string{r.Role, "inbox"} {
			if role == "" {
				continue
			}
			f, err := s.folderByRole(ctx, r.MailboxID, role)
			if err != nil {
				return f, err
			}
			if f.ID != "" && !f.Local {
				id = f.ID
				break
			}
		}
		if id == "" {
			return Folder{}, i18n.E("err.mail_recover_folder")
		}
	} else if err != nil {
		return Folder{}, err
	}
	return s.Folder(ctx, userID, id)
}

// ForgetRecoverable deletes the kept letters for good («Удалить навсегда» in «Восстановить удалённые элементы»).
func (s *Store) ForgetRecoverable(ctx context.Context, userID string, ids []string) (int, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_recoverable r USING mailboxes m WHERE m.id = r.mailbox_id AND m.user_id::text = $1
		AND r.id::text = ANY($2)`, userID, ids)
	return int(tag.RowsAffected()), err
}

// ExpireRecoverable forgets the letters kept over RecoverDays (the worker calls it).
func (s *Store) ExpireRecoverable(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM mail_recoverable WHERE deleted_at < now() - make_interval(days => $1)`, RecoverDays)
	return err
}
