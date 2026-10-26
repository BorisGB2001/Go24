package mail

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// Actions on letters, as in Outlook: read / unread, the flag (with a date to do it by; complete; clear), pin on top,
// categories, «Отложить» till a time, archive, delete, junk / not junk. Read and the flag go to the server too
// (\Seen, \Flagged); pin, the categories and snooze are go24's own and follow a letter moved to another folder.
// «Игнорировать беседу» (ignore, unignore: see quicksteps.go).
// «Переместить в Отсортированные / Другие» (focus, other; arg "always" — «Всегда перемещать») is go24's own too; a
// flag and a letter deleted unread teach go24 of the sender (see focus.go).

// Actions are what Act does.
var Actions = []string{"read", "unread", "flag", "done", "unflag", "pin", "unpin", "category", "uncategorize", "snooze", "unsnooze",
	"archive", "delete", "junk", "notjunk", "move", "focus", "other", "ignore", "unignore"}

// letter is what the actions need of a letter.
type letter struct {
	id, folder, name, role, mailbox, messageID string
	uid                                        int64
	local                                      bool
}

// letters are the user's letters of the ids (others are left out).
func (s *Store) letters(ctx context.Context, userID string, ids []string) ([]letter, error) {
	return s.lettersWhere(ctx, `x.id::text = ANY($1) AND m.user_id::text = $2`, ids, userID)
}

func (s *Store) lettersWhere(ctx context.Context, where string, args ...any) ([]letter, error) {
	rows, err := s.db.Query(ctx, `SELECT x.id::text, f.id::text, f.name, f.role, f.mailbox_id::text, x.message_id, x.uid, f.local
		FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE `+where+` ORDER BY f.mailbox_id, f.name, x.uid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []letter
	for rows.Next() {
		var l letter
		if err := rows.Scan(&l.id, &l.folder, &l.name, &l.role, &l.mailbox, &l.messageID, &l.uid, &l.local); err != nil {
			return nil, err
		}
		list = append(list, l)
	}
	return list, rows.Err()
}

// store sets (on) or takes off a flag of the letters on their servers; go24's own letters stay as they are.
func (s *Store) store(ctx context.Context, list []letter, flag string, on bool) error {
	var op imap.FlagsOp = imap.RemoveFlags
	if on {
		op = imap.AddFlags
	}
	for _, box := range byMailbox(list) {
		var remote []letter
		for _, l := range box {
			if !l.local && l.uid <= maxUID {
				remote = append(remote, l)
			}
		}
		if len(remote) == 0 {
			continue
		}
		err := s.remote(ctx, remote[0].mailbox, func(c *client.Client, a account) error {
			for _, folder := range byFolder(remote) {
				if _, err := c.Select(folder[0].name, false); err != nil {
					return err
				}
				set := new(imap.SeqSet)
				for _, l := range folder {
					set.AddNum(uint32(l.uid))
				}
				if err := c.UidStore(set, imap.FormatFlagsOp(op, true), []interface{}{flag}, nil); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return serverErr(err)
		}
	}
	return nil
}

// byMailbox and byFolder split letters (sorted by mailbox and folder) into runs.
func byMailbox(list []letter) [][]letter {
	return runs(list, func(l letter) string { return l.mailbox })
}
func byFolder(list []letter) [][]letter { return runs(list, func(l letter) string { return l.folder }) }

func runs(list []letter, key func(letter) string) (out [][]letter) {
	for i, l := range list {
		if i == 0 || key(list[i-1]) != key(l) {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], l)
	}
	return out
}

func ids(list []letter) []string {
	out := make([]string, len(list))
	for i, l := range list {
		out[i] = l.id
	}
	return out
}

// Act does an action on letters of the user. arg: the date to do a flagged letter by (YYYY-MM-DD, may be empty), the
// category's id, the time to snooze till (RFC 3339), the folder to move to.
func (s *Store) Act(ctx context.Context, userID string, letterIDs []string, action, arg string) error {
	list, err := s.letters(ctx, userID, letterIDs)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return ErrNotFound
	}
	all := ids(list)
	exec := func(sql string, args ...any) error {
		_, err := s.db.Exec(ctx, sql, append([]any{all}, args...)...)
		return err
	}
	switch action {
	case "read", "unread":
		if err := exec(`UPDATE mail_messages SET seen = $2 WHERE id::text = ANY($1)`, action == "read"); err != nil {
			return err
		}
		return s.store(ctx, list, imap.SeenFlag, action == "read")
	case "flag":
		var due *time.Time
		if arg != "" {
			d, err := time.Parse("2006-01-02", arg)
			if err != nil {
				return i18n.E("err.bad_date")
			}
			due = &d
		}
		if err := s.learnOf(ctx, userID, all, `NOT x.flagged`, 1); err != nil {
			return err
		}
		if err := exec(`UPDATE mail_messages SET flagged = true, flag_done = false, flag_due = $2 WHERE id::text = ANY($1)`, due); err != nil {
			return err
		}
		return s.store(ctx, list, imap.FlaggedFlag, true)
	case "done", "unflag":
		if err := exec(`UPDATE mail_messages SET flagged = false, flag_done = $2, flag_due = CASE WHEN $2 THEN flag_due END, flag_remind = NULL, flag_remind_by = NULL
			WHERE id::text = ANY($1)`, action == "done"); err != nil {
			return err
		}
		return s.store(ctx, list, imap.FlaggedFlag, false)
	case "remind": // as Outlook's «Добавить напоминание»: the letter gets a flag too
		at, err := time.Parse(time.RFC3339, arg)
		if err != nil || !at.After(time.Now()) {
			return i18n.E("err.mail_remind")
		}
		if err := s.learnOf(ctx, userID, all, `NOT x.flagged`, 1); err != nil {
			return err
		}
		if err := exec(`UPDATE mail_messages SET flagged = true, flag_done = false, flag_remind = $2, flag_remind_by = $3 WHERE id::text = ANY($1)`, at, userID); err != nil {
			return err
		}
		return s.store(ctx, list, imap.FlaggedFlag, true)
	case "unremind":
		return exec(`UPDATE mail_messages SET flag_remind = NULL, flag_remind_by = NULL WHERE id::text = ANY($1)`)
	case "pin", "unpin":
		return exec(`UPDATE mail_messages SET pinned = $2 WHERE id::text = ANY($1)`, action == "pin")
	case "category": // given to all the letters — or taken from all when all have it already
		if _, err := s.Category(ctx, userID, arg); err != nil {
			return err
		}
		return exec(`UPDATE mail_messages SET categories = CASE
				WHEN (SELECT bool_and(categories @> ARRAY[$2::uuid]) FROM mail_messages WHERE id::text = ANY($1)) THEN array_remove(categories, $2::uuid)
				WHEN categories @> ARRAY[$2::uuid] THEN categories ELSE categories || $2::uuid END
			WHERE id::text = ANY($1)`, arg)
	case "uncategorize":
		return exec(`UPDATE mail_messages SET categories = '{}' WHERE id::text = ANY($1)`)
	case "snooze":
		until, err := time.Parse(time.RFC3339, arg)
		if err != nil || !until.After(time.Now()) {
			return i18n.E("err.mail_snooze")
		}
		return exec(`UPDATE mail_messages SET snooze_until = $2 WHERE id::text = ANY($1)`, until)
	case "unsnooze":
		return exec(`UPDATE mail_messages SET snooze_until = NULL WHERE id::text = ANY($1)`)
	case "archive", "junk", "notjunk", "delete":
		if action == "delete" {
			if err := s.learnOf(ctx, userID, all, `NOT x.seen`, -1); err != nil {
				return err
			}
		}
		return s.file(ctx, userID, list, action)
	case "focus", "other":
		view := ViewFocused
		if action == "other" {
			view = ViewOther
		}
		return s.focus(ctx, userID, all, view, arg == "always")
	case "move": // arg: the folder
		return s.Move(ctx, userID, all, arg)
	case "ignore", "unignore":
		return s.ignore(ctx, userID, all, action == "ignore")
	}
	return i18n.E("err.bad_form")
}

// learnOf adds d to the scores of the senders of the letters that are so (cond: a condition on mail_messages) in the inbox.
func (s *Store) learnOf(ctx context.Context, userID string, ids []string, cond string, d int) error {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT lower(x.from_addr) FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id
		WHERE x.id::text = ANY($1) AND f.role = 'inbox' AND x.from_addr <> '' AND `+cond, ids)
	if err != nil {
		return err
	}
	from, err := pgxCollectStrings(rows)
	if err != nil {
		return err
	}
	return s.learn(ctx, userID, from, d)
}

// roleDefault is the name of a role folder go24 makes when the server has none.
var roleDefault = map[string]string{"archive": "Archive", "junk": "Junk", "trash": "Trash"}

// file moves letters by the action, each mailbox's to its own folder: archive → «Архив», junk → «Нежелательная
// почта», not junk → «Входящие», delete → «Удалённые» (made on the server when it has none); deleting in
// «Удалённые» deletes for good.
func (s *Store) file(ctx context.Context, userID string, list []letter, action string) error {
	role := map[string]string{"archive": "archive", "junk": "junk", "notjunk": "inbox", "delete": "trash"}[action]
	for _, box := range byMailbox(list) {
		var gone, move []letter
		for _, l := range box {
			switch {
			case l.role == role:
				if action == "delete" {
					gone = append(gone, l)
				}
			default:
				move = append(move, l)
			}
		}
		if len(gone) > 0 {
			if err := s.purge(ctx, gone); err != nil {
				return err
			}
		}
		if len(move) == 0 {
			continue
		}
		to, err := s.roleFolder(ctx, box[0].mailbox, role)
		if err != nil {
			return err
		}
		if err := s.Move(ctx, userID, ids(move), to.ID); err != nil {
			return err
		}
	}
	return nil
}

// roleFolder is the mailbox's folder of the role; one go24 makes on the server when there is none.
func (s *Store) roleFolder(ctx context.Context, mailboxID, role string) (Folder, error) {
	f, err := s.folderByRole(ctx, mailboxID, role)
	if err != nil || f.ID != "" {
		return f, err
	}
	name, ok := roleDefault[role]
	if !ok {
		return f, ErrNotFound
	}
	err = s.remote(ctx, mailboxID, func(c *client.Client, a account) error {
		if err := c.Create(name); err != nil && !s.folderExists(ctx, mailboxID, name) {
			return err
		}
		return s.syncFolders(ctx, c, mailboxID)
	})
	if err != nil {
		return f, serverErr(err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE mail_folders SET role = $3 WHERE mailbox_id::text = $1 AND name = $2 AND role = ''
		AND NOT EXISTS (SELECT 1 FROM mail_folders WHERE mailbox_id::text = $1 AND role = $3)`, mailboxID, name, role); err != nil {
		return f, err
	}
	return s.folderByRole(ctx, mailboxID, role)
}

// purge deletes letters for good: on the server (\Deleted, then EXPUNGE) and in go24.
func (s *Store) purge(ctx context.Context, list []letter) error {
	var remote []letter
	for _, l := range list {
		if !l.local && l.uid <= maxUID {
			remote = append(remote, l)
		}
	}
	if len(remote) > 0 {
		err := s.remote(ctx, remote[0].mailbox, func(c *client.Client, a account) error {
			for _, folder := range byFolder(remote) {
				if _, err := c.Select(folder[0].name, false); err != nil {
					return err
				}
				set := new(imap.SeqSet)
				for _, l := range folder {
					set.AddNum(uint32(l.uid))
				}
				if err := c.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil); err != nil {
					return err
				}
				if err := c.Expunge(nil); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return serverErr(err)
		}
	}
	_, err := s.db.Exec(ctx, `DELETE FROM mail_messages WHERE id::text = ANY($1)`, ids(list))
	return err
}

// kept is what go24 keeps of a letter on its own: it follows the letter moved to another folder (found there by
// its Message-ID).
type kept struct {
	MessageID            string
	Pinned, FlagDone     bool
	FlagDue, SnoozeUntil *time.Time
	WokeAt               *time.Time
	Categories           []string
	LeadID               *string
	FlagRemind           *time.Time
	FlagRemindBy         *string
	Unsubscribed         *time.Time
}

func (s *Store) keptOf(ctx context.Context, ids []string) ([]kept, error) {
	rows, err := s.db.Query(ctx, `SELECT message_id, pinned, flag_done, flag_due::timestamptz, snooze_until, woke_at, categories::text[], lead_id::text,
		flag_remind, flag_remind_by::text, unsubscribed_at FROM mail_messages WHERE id::text = ANY($1) AND message_id <> ''`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[kept])
}

func (s *Store) restore(ctx context.Context, folderID string, list []kept) error {
	for _, k := range list {
		if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET pinned = $3, flag_done = $4, flag_due = $5::date, snooze_until = $6, woke_at = $7,
			categories = $8::uuid[], lead_id = $9::uuid, flag_remind = $10, flag_remind_by = $11::uuid,
			unsubscribed_at = $12 WHERE folder_id::text = $1 AND message_id = $2`,
			folderID, k.MessageID, k.Pinned, k.FlagDone, k.FlagDue, k.SnoozeUntil, k.WokeAt, k.Categories, k.LeadID, k.FlagRemind, k.FlagRemindBy,
			k.Unsubscribed); err != nil {
			return err
		}
	}
	return nil
}

// WakeDue: letters whose snooze is over come back unread, on top of their folder (the worker calls it each minute).
func (s *Store) WakeDue(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `UPDATE mail_messages SET snooze_until = NULL, woke_at = now(), seen = false
		WHERE snooze_until <= now() RETURNING id::text`)
	if err != nil {
		return err
	}
	woke, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(woke) == 0 {
		return err
	}
	back, err := s.lettersWhere(ctx, `x.id::text = ANY($1)`, woke)
	if err != nil {
		return err
	}
	return s.store(ctx, back, imap.SeenFlag, false)
}

// RemindDue tells of the flags' reminders that came (the worker calls it each minute): whoever set one gets a
// notification that leads to the letter; the reminder is gone then, the flag stays.
func (s *Store) RemindDue(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `UPDATE mail_messages x SET flag_remind = NULL, flag_remind_by = NULL
		FROM mail_folders f JOIN mailboxes m ON m.id = f.mailbox_id, mail_messages o
		WHERE o.id = x.id AND f.id = x.folder_id AND x.flag_remind <= now()
		RETURNING x.id::text, f.id::text, m.id::text, m.user_id::text, coalesce(o.flag_remind_by::text, m.user_id::text), o.subject, o.from_name, o.from_addr`)
	if err != nil {
		return err
	}
	type due struct{ ID, Folder, Mailbox, Owner, To, Subject, FromName, FromAddr string }
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[due])
	if err != nil {
		return err
	}
	for _, d := range list {
		subject := d.Subject
		if subject == "" {
			subject = s.tr("mail.no_subject")
		}
		from := d.FromName
		if from == "" {
			from = d.FromAddr
		}
		link := "/mail?folder=" + d.Folder + "&letter=" + d.ID
		if d.To != d.Owner { // set in a shared mailbox
			link = "/mail/shared/" + d.Mailbox + "?letter=" + d.ID
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'mail_remind', left($2, 300), $3)`,
			d.To, from+" · "+subject, link); err != nil {
			return err
		}
	}
	return nil
}

// ---------- categories ----------

// Category is a colour category of the user's letters.
type Category struct {
	ID, Name, Color string
}

// Colors of the categories, as Outlook's.
var Colors = []string{"red", "orange", "yellow", "green", "teal", "blue", "purple", "gray"}

// MaxCategoryName bounds a category's name.
const MaxCategoryName = 60

// Categories of the user, by name; defaults (colour → name) are made the first time.
func (s *Store) Categories(ctx context.Context, userID string, defaults ...[2]string) ([]Category, error) {
	list, err := s.categories(ctx, userID)
	if err != nil || len(list) > 0 || len(defaults) == 0 {
		return list, err
	}
	var has bool // made once: a user who deleted them all has none
	if err := s.db.QueryRow(ctx, `SELECT mail_categories_made FROM users WHERE id::text = $1`, userID).Scan(&has); err != nil || has {
		return list, err
	}
	for _, d := range defaults {
		if _, err := s.db.Exec(ctx, `INSERT INTO mail_categories (user_id, name, color) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			userID, d[1], d[0]); err != nil {
			return nil, err
		}
	}
	if _, err := s.db.Exec(ctx, `UPDATE users SET mail_categories_made = true WHERE id::text = $1`, userID); err != nil {
		return nil, err
	}
	return s.categories(ctx, userID)
}

func (s *Store) categories(ctx context.Context, userID string) ([]Category, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, color FROM mail_categories WHERE user_id::text = $1 ORDER BY lower(name)`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Category])
}

// Category is one of the user's categories.
func (s *Store) Category(ctx context.Context, userID, id string) (Category, error) {
	var c Category
	err := s.db.QueryRow(ctx, `SELECT id::text, name, color FROM mail_categories WHERE id::text = $1 AND user_id::text = $2`, id, userID).
		Scan(&c.ID, &c.Name, &c.Color)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func checkCategory(name, color string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxCategoryName {
		return "", i18n.E("err.mail_category_name")
	}
	for _, c := range Colors {
		if c == color {
			return name, nil
		}
	}
	return "", i18n.E("err.bad_form")
}

func categoryErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return i18n.E("err.mail_category_exists")
	}
	return err
}

// SaveCategory makes a category of the user (id "") or changes one.
func (s *Store) SaveCategory(ctx context.Context, userID, id, name, color string) (string, error) {
	name, err := checkCategory(name, color)
	if err != nil {
		return "", err
	}
	if id == "" {
		err = s.db.QueryRow(ctx, `INSERT INTO mail_categories (user_id, name, color) VALUES ($1, $2, $3) RETURNING id::text`, userID, name, color).Scan(&id)
		return id, categoryErr(err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE mail_categories SET name = $3, color = $4 WHERE id::text = $1 AND user_id::text = $2`, id, userID, name, color)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return id, categoryErr(err)
}

// DeleteCategory deletes a category of the user; the letters lose it.
func (s *Store) DeleteCategory(ctx context.Context, userID, id string) error {
	if _, err := s.Category(ctx, userID, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE mail_messages x SET categories = array_remove(x.categories, $1::uuid) FROM mailboxes m
		WHERE m.id = x.mailbox_id AND m.user_id::text = $2 AND x.categories @> ARRAY[$1::uuid]`, id, userID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM mail_categories WHERE id::text = $1`, id)
	return err
}

// ---------- whole folders ----------

// ReadAll marks all letters of a folder of the user read, here and on the server (Outlook: «Отметить все как
// прочитанные»).
func (s *Store) ReadAll(ctx context.Context, userID, folderID string) error {
	list, err := s.lettersWhere(ctx, `f.id::text = $1 AND m.user_id::text = $2 AND NOT x.seen`, folderID, userID)
	if err != nil || len(list) == 0 {
		if _, ferr := s.Folder(ctx, userID, folderID); ferr != nil {
			return ferr
		}
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET seen = true WHERE id::text = ANY($1)`, ids(list)); err != nil {
		return err
	}
	return s.store(ctx, list, imap.SeenFlag, true)
}

// Empty deletes all letters of the user's «Удалённые» or «Нежелательная почта» for good (Outlook: «Очистить папку»).
func (s *Store) Empty(ctx context.Context, userID, folderID string) error {
	f, err := s.Folder(ctx, userID, folderID)
	if err != nil {
		return err
	}
	if f.Role != "trash" && f.Role != "junk" {
		return i18n.E("err.mail_empty_role")
	}
	list, err := s.lettersWhere(ctx, `f.id::text = $1 AND m.user_id::text = $2`, folderID, userID)
	if err != nil || len(list) == 0 {
		return err
	}
	return s.purge(ctx, list)
}
