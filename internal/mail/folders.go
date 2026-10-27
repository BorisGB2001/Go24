package mail

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Folders of a mailbox, as in Outlook: the IMAP folders with their roles (Входящие, Черновики, Отправленные,
// Удалённые, Спам, Архив) first, then the user's own as a tree. Creating, renaming and deleting a folder and moving
// letters are done on the server first, then in go24.

// Roles in the order the folder list shows them.
var Roles = []string{"inbox", "drafts", "sent", "trash", "junk", "archive"}

// Folder is a folder of a mailbox.
type Folder struct {
	ID, MailboxID, Name, Delim, Role string
	Selectable, Local                bool
	Unseen, Total                    int
}

// Title is the folder's own name (the last part of its path).
func (f Folder) Title() string {
	if f.Delim != "" {
		if i := strings.LastIndex(f.Name, f.Delim); i >= 0 {
			return f.Name[i+len(f.Delim):]
		}
	}
	return f.Name
}

// Depth is how deep the folder is in the tree (0 — at the top).
func (f Folder) Depth() int {
	if f.Delim == "" || f.Role == "inbox" {
		return 0
	}
	return strings.Count(f.Name, f.Delim)
}

// Own: the user's folder (not a role one) — it may be renamed and deleted.
func (f Folder) Own() bool { return f.Role == "" && !f.Local && !strings.EqualFold(f.Name, "INBOX") }

const folderSQL = `SELECT f.id::text, f.mailbox_id::text, f.name, f.delim, f.role, f.selectable, f.local,
		(SELECT count(*) FROM mail_messages x WHERE x.folder_id = f.id AND NOT x.seen)::int,
		(SELECT count(*) FROM mail_messages x WHERE x.folder_id = f.id)::int
	FROM mail_folders f JOIN mailboxes m ON m.id = f.mailbox_id`

const folderOrder = ` ORDER BY m.email, coalesce(array_position(ARRAY['inbox','drafts','sent','trash','junk','archive'], nullif(f.role, '')), 99), lower(f.name)`

// Folders of all the user's mailboxes (or of one), each mailbox's in the order of the folder list.
func (s *Store) Folders(ctx context.Context, userID, mailboxID string) ([]Folder, error) {
	rows, err := s.db.Query(ctx, folderSQL+` WHERE m.user_id::text = $1 AND ($2 = '' OR m.id::text = $2)`+folderOrder, userID, mailboxID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Folder])
}

// Folder is one folder of the user's mailboxes.
func (s *Store) Folder(ctx context.Context, userID, id string) (Folder, error) {
	rows, err := s.db.Query(ctx, folderSQL+` WHERE m.user_id::text = $1 AND f.id::text = $2`, userID, id)
	if err != nil {
		return Folder{}, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Folder])
	if err != nil {
		return Folder{}, err
	}
	if len(list) == 0 {
		return Folder{}, ErrNotFound
	}
	return list[0], nil
}

// folderByRole is the mailbox's folder of the role ("" ID — none).
func (s *Store) folderByRole(ctx context.Context, mailboxID, role string) (Folder, error) {
	var f Folder
	err := s.db.QueryRow(ctx, `SELECT id::text, mailbox_id::text, name, delim, role, selectable, local FROM mail_folders
		WHERE mailbox_id::text = $1 AND role = $2 ORDER BY local, name LIMIT 1`, mailboxID, role).
		Scan(&f.ID, &f.MailboxID, &f.Name, &f.Delim, &f.Role, &f.Selectable, &f.Local)
	if errors.Is(err, pgx.ErrNoRows) {
		return Folder{}, nil
	}
	return f, err
}

// roleNames are the usual names of the role folders (servers without SPECIAL-USE), lower case.
var roleNames = map[string][]string{
	"sent":    {"sent", "sent items", "sent messages", "sent mail", "отправленные", "исходящие"},
	"drafts":  {"drafts", "draft", "черновики"},
	"trash":   {"trash", "deleted", "deleted items", "deleted messages", "bin", "удаленные", "удалённые", "корзина"},
	"junk":    {"junk", "spam", "junk e-mail", "junk mail", "bulk mail", "спам", "нежелательная почта"},
	"archive": {"archive", "archives", "архив"},
}

// roleOf is the folder's role by its special-use attribute or, failing that, by its usual name (byName).
func roleOf(info *imap.MailboxInfo) (role string, byName bool) {
	if strings.EqualFold(info.Name, "INBOX") {
		return "inbox", false
	}
	for _, a := range info.Attributes {
		switch a {
		case imap.SentAttr:
			return "sent", false
		case imap.DraftsAttr:
			return "drafts", false
		case imap.TrashAttr:
			return "trash", false
		case imap.JunkAttr:
			return "junk", false
		case imap.ArchiveAttr:
			return "archive", false
		}
	}
	title := strings.ToLower(info.Name)
	if info.Delimiter != "" {
		if strings.Count(info.Name, info.Delimiter) > 1 { // only at the top or right under INBOX
			return "", false
		}
		if i := strings.LastIndex(title, strings.ToLower(info.Delimiter)); i >= 0 {
			if !strings.EqualFold(info.Name[:i], "INBOX") {
				return "", false
			}
			title = title[i+len(info.Delimiter):]
		}
	}
	for role, names := range roleNames {
		for _, n := range names {
			if title == n {
				return role, true
			}
		}
	}
	return "", false
}

// syncFolders brings the server's folder list into go24: new folders appear, gone ones go (with their letters),
// each role is given to one folder (a special-use one before one found by its name).
func (s *Store) syncFolders(ctx context.Context, c *client.Client, mailboxID string) error {
	ch := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- c.List("", "*", ch) }()
	var infos []*imap.MailboxInfo
	for info := range ch {
		infos = append(infos, info)
	}
	if err := <-done; err != nil {
		return err
	}
	roles := make([]string, len(infos))
	taken := map[string]bool{}
	for pass := 0; pass < 2; pass++ { // special-use and INBOX first, then by name
		for i, info := range infos {
			role, byName := roleOf(info)
			if role == "" || byName != (pass == 1) || taken[role] {
				continue
			}
			roles[i], taken[role] = role, true
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	names := make([]string, 0, len(infos))
	for i, info := range infos {
		selectable := true
		for _, a := range info.Attributes {
			if a == imap.NoSelectAttr || a == "\\NonExistent" {
				selectable = false
			}
		}
		name := info.Name
		if roles[i] == "inbox" {
			name = "INBOX"
		}
		names = append(names, name)
		if _, err := tx.Exec(ctx, `INSERT INTO mail_folders (mailbox_id, name, delim, role, selectable) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (mailbox_id, name) DO UPDATE SET delim = EXCLUDED.delim, role = EXCLUDED.role, selectable = EXCLUDED.selectable, local = false`,
			mailboxID, name, info.Delimiter, roles[i], selectable); err != nil {
			return err
		}
	}
	// go24's own folders give their role up to a server one
	if _, err := tx.Exec(ctx, `UPDATE mail_folders l SET role = '' WHERE l.mailbox_id = $1 AND l.local AND l.role <> ''
		AND EXISTS (SELECT 1 FROM mail_folders f WHERE f.mailbox_id = l.mailbox_id AND NOT f.local AND f.role = l.role)`, mailboxID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mail_folders WHERE mailbox_id = $1 AND NOT local AND NOT (name = ANY($2))`, mailboxID, names); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// remote runs fn signed in to the mailbox's IMAP server.
func (s *Store) remote(ctx context.Context, mailboxID string, fn func(c *client.Client, a account) error) error {
	a, err := s.account(ctx, mailboxID)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, &a); err != nil {
		return err
	}
	c, err := dialIMAP(a.IMAPHost, a.IMAPPort, a.TLS)
	if err != nil {
		return i18n.E("err.mail_connect", err.Error())
	}
	defer c.Logout()
	if err := imapLogin(c, a); err != nil {
		if a.oauth != "" {
			return i18n.E("err.mail_oauth_again")
		}
		return i18n.E("err.mail_login")
	}
	err = fn(c, a)
	// leave no folder of the user's selected: a server may trip over a folder of a closed session when it is
	// deleted later (GreenMail does); without UNSELECT the inbox, never deleted, is the one left
	if mb := c.Mailbox(); mb != nil && !strings.EqualFold(mb.Name, "INBOX") {
		if ok, _ := c.Support("UNSELECT"); ok {
			_ = c.Unselect()
		} else {
			_, _ = c.Select("INBOX", true)
		}
	}
	return err
}

// MaxFolderName bounds a folder's own name.
const MaxFolderName = 100

func (s *Store) folderName(ctx context.Context, mailboxID, name string) (string, error) {
	name = strings.TrimSpace(name)
	var delim string
	_ = s.db.QueryRow(ctx, `SELECT delim FROM mail_folders WHERE mailbox_id::text = $1 AND delim <> '' LIMIT 1`, mailboxID).Scan(&delim)
	if delim == "" {
		delim = "/"
	}
	if name == "" || utf8.RuneCountInString(name) > MaxFolderName || strings.Contains(name, delim) || strings.ContainsAny(name, "*%\r\n") ||
		strings.EqualFold(name, "INBOX") {
		return "", i18n.E("err.mail_folder_name")
	}
	return name, nil
}

func (s *Store) folderExists(ctx context.Context, mailboxID, name string) bool {
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_folders WHERE mailbox_id::text = $1 AND lower(name) = lower($2))`, mailboxID, name).Scan(&ok)
	return ok
}

// CreateFolder makes a folder of the user's mailbox (inside parentID, or at the top).
func (s *Store) CreateFolder(ctx context.Context, userID, mailboxID, parentID, name string) (string, error) {
	if _, err := s.Mailbox(ctx, userID, mailboxID); err != nil {
		return "", err
	}
	name, err := s.folderName(ctx, mailboxID, name)
	if err != nil {
		return "", err
	}
	full, delim := name, "/"
	if parentID != "" {
		p, err := s.Folder(ctx, userID, parentID)
		if err != nil || p.MailboxID != mailboxID || p.Local {
			return "", ErrNotFound
		}
		if p.Delim != "" {
			delim = p.Delim
		}
		full = p.Name + delim + name
	} else if f, _ := s.folderByRole(ctx, mailboxID, "inbox"); f.Delim != "" {
		delim = f.Delim
	}
	if s.folderExists(ctx, mailboxID, full) {
		return "", i18n.E("err.mail_folder_exists")
	}
	if err := s.remote(ctx, mailboxID, func(c *client.Client, a account) error { return c.Create(full) }); err != nil {
		return "", serverErr(err)
	}
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, delim) VALUES ($1, $2, $3)
		ON CONFLICT (mailbox_id, name) DO UPDATE SET local = false RETURNING id::text`, mailboxID, full, delim).Scan(&id)
	return id, err
}

// RenameFolder gives the user's own folder a new name (its sub-folders follow it).
func (s *Store) RenameFolder(ctx context.Context, userID, id, name string) error {
	f, err := s.Folder(ctx, userID, id)
	if err != nil {
		return err
	}
	if !f.Own() {
		return i18n.E("err.mail_folder_system")
	}
	if name, err = s.folderName(ctx, f.MailboxID, name); err != nil {
		return err
	}
	full := name
	if i := strings.LastIndex(f.Name, f.Delim); f.Delim != "" && i >= 0 {
		full = f.Name[:i+len(f.Delim)] + name
	}
	if full == f.Name {
		return nil
	}
	if s.folderExists(ctx, f.MailboxID, full) {
		return i18n.E("err.mail_folder_exists")
	}
	if err := s.remote(ctx, f.MailboxID, func(c *client.Client, a account) error { return c.Rename(f.Name, full) }); err != nil {
		return serverErr(err)
	}
	_, err = s.db.Exec(ctx, `UPDATE mail_folders SET name = $3 || substr(name, length($2) + 1)
		WHERE mailbox_id::text = $1 AND (name = $2 OR (delim <> '' AND starts_with(name, $2 || delim)))`, f.MailboxID, f.Name, full)
	return err
}

// DeleteFolder deletes the user's own folder with its letters (one with sub-folders is emptied of them first).
func (s *Store) DeleteFolder(ctx context.Context, userID, id string) error {
	f, err := s.Folder(ctx, userID, id)
	if err != nil {
		return err
	}
	if !f.Own() {
		return i18n.E("err.mail_folder_system")
	}
	var children bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_folders WHERE mailbox_id::text = $1 AND delim <> '' AND starts_with(name, $2 || delim))`,
		f.MailboxID, f.Name).Scan(&children); err != nil {
		return err
	}
	if children {
		return i18n.E("err.mail_folder_children")
	}
	if err := s.remote(ctx, f.MailboxID, func(c *client.Client, a account) error { return c.Delete(f.Name) }); err != nil {
		return serverErr(err)
	}
	_, err = s.db.Exec(ctx, `DELETE FROM mail_folders WHERE id::text = $1`, f.ID)
	return err
}

// Move moves letters of the user to a folder of the same mailbox (drag and drop, «Переместить»): on the server,
// then the folder is fetched so the letters come with their new numbers.
func (s *Store) Move(ctx context.Context, userID string, ids []string, folderID string) error {
	to, err := s.Folder(ctx, userID, folderID)
	if err != nil {
		return err
	}
	if !to.Selectable {
		return i18n.E("err.mail_folder_noselect")
	}
	rows, err := s.db.Query(ctx, `SELECT x.id::text, x.uid, f.id::text, f.name, f.local, f.mailbox_id::text
		FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE x.id::text = ANY($1) AND m.user_id::text = $2`, ids, userID)
	if err != nil {
		return err
	}
	type letter struct {
		id      string
		uid     int64
		folder  string
		name    string
		local   bool
		mailbox string
	}
	from := map[string][]letter{}
	var order []string
	n := 0
	for rows.Next() {
		var l letter
		if err := rows.Scan(&l.id, &l.uid, &l.folder, &l.name, &l.local, &l.mailbox); err != nil {
			rows.Close()
			return err
		}
		n++
		if l.mailbox != to.MailboxID {
			rows.Close()
			return i18n.E("err.mail_move_box")
		}
		if l.folder == to.ID {
			continue
		}
		if _, ok := from[l.folder]; !ok {
			order = append(order, l.folder)
		}
		from[l.folder] = append(from[l.folder], l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if len(order) == 0 {
		return nil
	}
	var local, remote []letter
	for _, fid := range order {
		for _, l := range from[fid] {
			if l.local || to.Local || l.uid > maxUID { // go24's own letters (drafts too) are not on the server
				local = append(local, l)
			} else {
				remote = append(remote, l)
			}
		}
	}
	for _, l := range local { // go24's own letters move in go24 only
		if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET folder_id = $2 WHERE id::text = $1`, l.id, to.ID); err != nil {
			return err
		}
		if to.Role != "drafts" { // a draft moved out of «Черновики» (deleted) is just a letter there
			if _, err := s.db.Exec(ctx, `DELETE FROM mail_drafts WHERE id::text = $1 AND NOT sending`, l.id); err != nil {
				return err
			}
		}
	}
	if len(remote) == 0 {
		return nil
	}
	return s.remote(ctx, to.MailboxID, func(c *client.Client, a account) error {
		var keep []kept // go24's own marks follow the letters
		for _, fid := range order {
			set := new(imap.SeqSet)
			var moved []string
			for _, l := range from[fid] {
				if !l.local && l.uid <= maxUID {
					set.AddNum(uint32(l.uid))
					moved = append(moved, l.id)
				}
			}
			if len(moved) == 0 {
				continue
			}
			if _, err := c.Select(from[fid][0].name, false); err != nil {
				return serverErr(err)
			}
			if err := c.UidMove(set, to.Name); err != nil {
				return serverErr(err)
			}
			k, err := s.keptOf(ctx, moved)
			if err != nil {
				return err
			}
			keep = append(keep, k...)
			if _, err := s.db.Exec(ctx, `DELETE FROM mail_messages WHERE id::text = ANY($1)`, moved); err != nil {
				return err
			}
		}
		if err := s.syncFolder(ctx, c, a, to); err != nil {
			return err
		}
		return s.restore(ctx, to.ID, keep)
	})
}

// serverErr: what the mail server said, for the user (an i18n error passes as it is).
func serverErr(err error) error {
	var ie *i18n.Error
	if errors.As(err, &ie) {
		return err
	}
	return i18n.E("err.mail_server_said", clip(err.Error(), 300))
}
