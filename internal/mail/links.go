package mail

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
)

// A letter's links with the rest of go24 mail has them: tasks made of it (with its files and a link
// back), CRM cards it belongs to (their timeline keeps a copy: crm_mail; later letters of the conversation join the
// card by themselves).

// ReadFile is a file of the user's letters with its bytes (to copy it to a task).
func (s *Store) ReadFile(ctx context.Context, userID, fileID string) (Attachment, error) {
	return s.readFile(ctx, userID, fileID)
}

// LinkTask remembers that the task was made of the user's letter.
func (s *Store) LinkTask(ctx context.Context, userID, letterID, taskID string) error {
	tag, err := s.db.Exec(ctx, `INSERT INTO mail_task_links (task_id, message_id, user_id, subject, from_addr, from_name, sent_at)
		SELECT $3::text::uuid, x.id, $2::text::uuid, x.subject, x.from_addr, x.from_name, x.sent_at FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2 ON CONFLICT DO NOTHING`, letterID, userID, taskID)
	if err == nil && tag.RowsAffected() == 0 && !s.Owns(ctx, userID, letterID) {
		err = ErrNotFound
	}
	return err
}

// TaskRef is a task linked to a letter.
type TaskRef struct {
	ID, Key, Title, Status string
}

// LetterTasks are the tasks made of the letters of the user's letter's conversation.
func (s *Store) LetterTasks(ctx context.Context, userID string, letterIDs []string) ([]TaskRef, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT t.id::text, p.key || '-' || t.number, t.title, t.status, t.created_at FROM mail_task_links l
		JOIN tasks t ON t.id = l.task_id JOIN projects p ON p.id = t.project_id
		JOIN mail_messages x ON x.id = l.message_id JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE l.message_id::text = ANY($1) AND m.user_id::text = $2 ORDER BY t.created_at`, letterIDs, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TaskRef, error) {
		var t TaskRef
		var at time.Time
		err := r.Scan(&t.ID, &t.Key, &t.Title, &t.Status, &at)
		return t, err
	})
}

// TaskLetter is a letter a task was made of, as the task's card shows it.
type TaskLetter struct {
	LetterID, OwnerID, Subject, FromAddr, FromName string // LetterID "" — the letter is gone from the mailbox
	SentAt                                         time.Time
}

// TaskLetters are the letters the task was made of.
func (s *Store) TaskLetters(ctx context.Context, taskID string) ([]TaskLetter, error) {
	rows, err := s.db.Query(ctx, `SELECT coalesce(message_id::text, ''), user_id::text, subject, from_addr, from_name, sent_at
		FROM mail_task_links WHERE task_id::text = $1 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[TaskLetter])
}

// CRMRef is a CRM card a letter belongs to.
type CRMRef struct {
	LinkID, Entity, EntityID, Title string
}

// Path is the card's address in go24.
func (c CRMRef) Path() string {
	return "/crm/" + map[string]string{"deal": "deals", "lead": "leads", "contact": "contacts", "company": "companies"}[c.Entity] + "/" + c.EntityID
}

// crmTitle is a card's title (a contact's name).
const crmTitle = `CASE c.entity WHEN 'lead' THEN (SELECT title FROM crm_leads WHERE id = c.entity_id)
	WHEN 'deal' THEN (SELECT title FROM crm_deals WHERE id = c.entity_id)
	WHEN 'contact' THEN (SELECT name FROM crm_contacts WHERE id = c.entity_id)
	ELSE (SELECT title FROM crm_companies WHERE id = c.entity_id) END`

// LinkCRM puts the user's letter into a CRM card's timeline (once); new — it was not there.
func (s *Store) LinkCRM(ctx context.Context, userID, letterID, entity, entityID string) (bool, error) {
	if s.table(entity) == "" {
		return false, ErrNotFound
	}
	var id string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO crm_mail (entity, entity_id, message_id, mailbox_id, thread, outgoing, from_addr, from_name, to_addr,
				subject, body, sent_at, linked_by)
			SELECT $3::text, $4::text::uuid, x.id, x.mailbox_id, x.thread, f.role IN ('sent', 'drafts'), x.from_addr, x.from_name, x.to_addr, x.subject,
				left(x.body, 20000), x.sent_at, $2::text::uuid FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = x.mailbox_id
			WHERE x.id::text = $1 AND m.user_id::text = $2 ON CONFLICT DO NOTHING RETURNING id::text`, letterID, userID, entity, entityID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO crm_timeline (entity, entity_id, author_id, kind, body) VALUES ($1, $2, $3, 'mail', $4)`,
			entity, entityID, userID, id)
		return err
	})
	if err == nil && id == "" && !s.Owns(ctx, userID, letterID) {
		err = ErrNotFound
	}
	return id != "", err
}

func (s *Store) table(entity string) string {
	return map[string]string{"lead": "crm_leads", "deal": "crm_deals", "contact": "crm_contacts", "company": "crm_companies"}[entity]
}

// LetterCRM are the CRM cards the letters belong to.
func (s *Store) LetterCRM(ctx context.Context, userID string, letterIDs []string) ([]CRMRef, error) {
	rows, err := s.db.Query(ctx, `SELECT min(c.id::text), c.entity, c.entity_id::text, coalesce(`+crmTitle+`, '') FROM crm_mail c
		JOIN mail_messages x ON x.id = c.message_id JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE c.message_id::text = ANY($1) AND m.user_id::text = $2 GROUP BY c.entity, c.entity_id ORDER BY c.entity, 4`, letterIDs, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[CRMRef])
}

// CRMCatchUp puts into a card the letters of the conversations it has that came (or went) since: the timeline shows
// the whole correspondence binds the answers to the card.
func (s *Store) CRMCatchUp(ctx context.Context, entity, entityID string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `INSERT INTO crm_mail (entity, entity_id, message_id, mailbox_id, thread, outgoing, from_addr, from_name, to_addr,
				subject, body, sent_at, linked_by)
			SELECT DISTINCT ON (x.id) $1::text, $2::text::uuid, x.id, x.mailbox_id, x.thread, f.role IN ('sent', 'drafts'), x.from_addr, x.from_name, x.to_addr,
				x.subject, left(x.body, 20000), x.sent_at, m.user_id
			FROM crm_mail c JOIN mail_messages x ON x.mailbox_id = c.mailbox_id AND x.thread = c.thread
			JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = x.mailbox_id
			LEFT JOIN mail_drafts d ON d.id = x.id
			WHERE c.entity = $1 AND c.entity_id::text = $2 AND c.thread <> '' AND d.id IS NULL AND f.role NOT IN ('trash', 'junk')
			ON CONFLICT DO NOTHING RETURNING id::text, linked_by::text`, entity, entityID)
		if err != nil {
			return err
		}
		type added struct{ id, by string }
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (added, error) {
			var a added
			err := r.Scan(&a.id, &a.by)
			return a, err
		})
		if err != nil {
			return err
		}
		for _, a := range list {
			if _, err := tx.Exec(ctx, `INSERT INTO crm_timeline (entity, entity_id, author_id, kind, body, created_at)
				SELECT $1, $2, $3, 'mail', $4, sent_at FROM crm_mail WHERE id::text = $4`, entity, entityID, a.by, a.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// UnlinkCRM takes a letter's conversation out of a card's timeline (else its letters would come back by themselves).
func (s *Store) UnlinkCRM(ctx context.Context, entity, entityID, linkID string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM crm_mail c USING crm_mail l WHERE l.id::text = $1 AND l.entity = $2 AND l.entity_id::text = $3
			AND c.entity = l.entity AND c.entity_id = l.entity_id
			AND (c.id = l.id OR (l.thread <> '' AND c.thread = l.thread AND c.mailbox_id = l.mailbox_id)) RETURNING c.id::text`, linkID, entity, entityID)
		if err != nil {
			return err
		}
		ids, err := pgxCollectStrings(rows)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `DELETE FROM crm_timeline WHERE kind = 'mail' AND body = ANY($1)`, ids)
		return err
	})
}

// ShareRef is where a letter was shared: a post of the feed or a chat.
type ShareRef struct {
	Kind, Target, Title string // Kind post | chat
}

// Path is the share's address in go24.
func (r ShareRef) Path() string {
	if r.Kind == "post" {
		return "/feed#post-" + r.Target
	}
	return "/im?room=" + url.QueryEscape(r.Target)
}

// LinkShare remembers that the user's letter went to a post (kind post) or a chat (kind chat).
func (s *Store) LinkShare(ctx context.Context, userID, letterID, kind, target, title string) error {
	tag, err := s.db.Exec(ctx, `INSERT INTO mail_letter_shares (message_id, user_id, kind, target, title)
		SELECT x.id, $2::text::uuid, $3, $4, $5 FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2 ON CONFLICT (message_id, kind, target) DO UPDATE SET title = EXCLUDED.title`,
		letterID, userID, kind, target, title)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// LetterShares are the posts and chats the letters of the user's letter's conversation went to.
func (s *Store) LetterShares(ctx context.Context, userID string, letterIDs []string) ([]ShareRef, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT ON (kind, target) kind, target, title FROM mail_letter_shares
		WHERE message_id::text = ANY($1) AND user_id::text = $2 ORDER BY kind, target, created_at DESC`, letterIDs, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ShareRef])
}
