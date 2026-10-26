package mail

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/pst"
)

// «Импорт из файла», as Outlook's «Импорт и экспорт → Импорт из другой программы или файла»: Outlook's personal
// folders (.pst), an .mbox file (Thunderbird, Apple Mail), a .zip of .mbox or .eml files (go24's own «Экспорт») or one
// .eml go into a mailbox. The folders of the file go into the same folders of the mailbox — «Входящие»,
// «Отправленные», «Удалённые»… by their roles, the rest made as a tree, — or all of them into a chosen folder; letters
// already there (by Message-ID) are left out unless the user asks for duplicates. go24 worker puts the letters on the
// server as they are read from the file, so a file of gigabytes never sits in memory; a restarted import goes on
// where it stopped. Contacts, meetings, tasks and notes of a .pst are no letters: they are counted as left out.

// ImportFormats are the files «Импорт из файла» takes.
var ImportFormats = []string{"pst", "mbox", "zip", "eml"}

// MaxImportSize bounds the file of an import.
const MaxImportSize = 20 << 30

// MaxImportsQueued bounds the imports of a user waiting or running at once.
const MaxImportsQueued = 3

// FileImport is an import of letters from a file.
type FileImport struct {
	ID, UserID, MailboxID, Email, FolderID, FileID, Name string
	Format, Lang, Status                                 string
	Dupes                                                bool
	ImportCount
	Error                string
	CreatedAt, UpdatedAt time.Time
	FinishedAt           *time.Time
	Tr                   func(key string, args ...any) string // the name of «Входящие» made as a subfolder
}

// ImportCount is how far an import went.
type ImportCount struct {
	Letters, Same, Skipped, Position, Folders int
}

// Busy: the import waits or runs.
func (e FileImport) Busy() bool { return e.Status == "queued" || e.Status == "running" }

// NewImport is what the user asks to import: a file of the drive, where to.
type NewImport struct {
	MailboxID, FolderID, FileID, Name, Format, Lang string
	Dupes                                           bool
}

// ImportFormat is the kind of the file by its first bytes and its name ("" — none «Импорт из файла» takes).
func ImportFormat(name string, head []byte) string {
	switch ext := strings.ToLower(path.Ext(name)); {
	case bytes.HasPrefix(head, []byte("!BDN")):
		return "pst"
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return "zip"
	case bytes.HasPrefix(head, []byte("From ")):
		return "mbox"
	case ext == ".eml" || ext == ".mbox" || ext == ".mbx":
		if ext == ".eml" {
			return "eml"
		}
		return "mbox"
	}
	return ""
}

const importSQL = `SELECT e.id::text, e.user_id::text, e.mailbox_id::text, m.email, coalesce(e.folder_id::text, ''), coalesce(e.file_id::text, ''),
		e.name, e.format, e.lang, e.status, e.dupes, e.letters, e.same, e.skipped, e.position, e.folders, e.error, e.created_at, e.updated_at, e.finished_at
	FROM mail_imports e JOIN mailboxes m ON m.id = e.mailbox_id`

func scanImport(row pgx.Row) (FileImport, error) {
	var e FileImport
	err := row.Scan(&e.ID, &e.UserID, &e.MailboxID, &e.Email, &e.FolderID, &e.FileID, &e.Name, &e.Format, &e.Lang, &e.Status, &e.Dupes,
		&e.Letters, &e.Same, &e.Skipped, &e.Position, &e.Folders, &e.Error, &e.CreatedAt, &e.UpdatedAt, &e.FinishedAt)
	return e, err
}

// AddImport puts an import in the queue of go24 worker. The caller has decided the user may read the file.
func (s *Store) AddImport(ctx context.Context, userID string, n NewImport) (string, error) {
	if _, err := s.Mailbox(ctx, userID, n.MailboxID); err != nil {
		return "", err
	}
	if !validImportFormat(n.Format) {
		return "", i18n.E("err.mail_import_format")
	}
	if n.FolderID != "" {
		f, err := s.Folder(ctx, userID, n.FolderID)
		if err != nil {
			return "", err
		}
		if f.MailboxID != n.MailboxID {
			return "", ErrNotFound
		}
		if !f.Selectable || f.Local {
			return "", i18n.E("err.mail_import_folder")
		}
	}
	var busy int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM mail_imports WHERE user_id = $1 AND status IN ('queued', 'running')`,
		userID).Scan(&busy); err != nil {
		return "", err
	}
	if busy >= MaxImportsQueued {
		return "", i18n.E("err.mail_import_busy", MaxImportsQueued)
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO mail_imports (user_id, mailbox_id, folder_id, file_id, name, format, dupes, lang)
		VALUES ($1, $2, nullif($3, '')::uuid, nullif($4, '')::uuid, left($5, 255), $6, $7, $8) RETURNING id::text`,
		userID, n.MailboxID, n.FolderID, n.FileID, n.Name, n.Format, n.Dupes, n.Lang).Scan(&id)
	return id, err
}

func validImportFormat(f string) bool {
	for _, x := range ImportFormats {
		if x == f {
			return true
		}
	}
	return false
}

// Imports are the user's imports, the latest first.
func (s *Store) Imports(ctx context.Context, userID string) ([]FileImport, error) {
	rows, err := s.db.Query(ctx, importSQL+` WHERE e.user_id::text = $1 ORDER BY e.created_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (FileImport, error) { return scanImport(r) })
}

// GetImport is one import of the user.
func (s *Store) GetImport(ctx context.Context, userID, id string) (FileImport, error) {
	e, err := scanImport(s.db.QueryRow(ctx, importSQL+` WHERE e.id::text = $1 AND e.user_id::text = $2`, id, userID))
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return e, err
}

// DeleteImport takes an import off the list (a waiting one is not made); the letters it put stay.
func (s *Store) DeleteImport(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_imports WHERE id::text = $1 AND user_id::text = $2 AND status <> 'running'`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// ClaimImport takes the next import of the queue (ok false — none); a stale running one goes on from its position.
func (s *Store) ClaimImport(ctx context.Context) (e FileImport, ok bool, err error) {
	var id string
	err = s.db.QueryRow(ctx, `UPDATE mail_imports SET status = 'running', updated_at = now()
		WHERE id = (SELECT id FROM mail_imports WHERE status = 'queued' OR (status = 'running' AND updated_at < now() - $1::interval)
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id::text`, fmt.Sprintf("%d seconds", int(exportStale.Seconds()))).Scan(&id)
	if err == pgx.ErrNoRows {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	e, err = scanImport(s.db.QueryRow(ctx, importSQL+` WHERE e.id::text = $1`, id))
	return e, err == nil, err
}

// ImportProgress records how far the running import went (and that it is alive).
func (s *Store) ImportProgress(ctx context.Context, id string, n ImportCount) error {
	_, err := s.db.Exec(ctx, `UPDATE mail_imports SET letters = $2, same = $3, skipped = $4, position = $5, folders = $6, updated_at = now()
		WHERE id::text = $1 AND status = 'running'`, id, n.Letters, n.Same, n.Skipped, n.Position, n.Folders)
	return err
}

// FinishImport records how the import ended.
func (s *Store) FinishImport(ctx context.Context, id string, n ImportCount, errText string) error {
	status := "done"
	if errText != "" {
		status = "failed"
	}
	_, err := s.db.Exec(ctx, `UPDATE mail_imports SET status = $2, letters = $3, same = $4, skipped = $5, position = $6, folders = $7,
		error = left($8, 500), updated_at = now(), finished_at = now() WHERE id::text = $1`,
		id, status, n.Letters, n.Same, n.Skipped, n.Position, n.Folders, errText)
	return err
}

// fileLetter is a letter of the file: its folder there, the letter as a mail server keeps it (nil — no letter).
type fileLetter struct {
	path  []string
	msg   []byte
	date  time.Time
	flags []string
}

// RunImport puts the letters of the import's file (f, a local copy) into the mailbox, telling progress how far it
// went now and then.
func (s *Store) RunImport(ctx context.Context, e FileImport, f *os.File, progress func(ImportCount)) (ImportCount, error) {
	n := e.ImportCount
	a, err := s.account(ctx, e.MailboxID)
	if err != nil {
		return n, err
	}
	host := a.Email[strings.LastIndex(a.Email, "@")+1:]
	var into Folder
	if e.FolderID != "" {
		if into, err = s.Folder(ctx, e.UserID, e.FolderID); err != nil {
			return n, i18n.E("err.mail_import_gone")
		}
	}
	alive := time.Now()
	err = s.remote(ctx, e.MailboxID, func(c *client.Client, a account) error {
		t, err := s.newImportTarget(ctx, c, e.MailboxID, into)
		t.tr = e.Tr
		if err != nil {
			return err
		}
		pos := 0
		err = fileLetters(e.Format, f, host, func(l fileLetter) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if pos++; pos <= e.Position { // put before the import was stopped
				return nil
			}
			n.Position = pos
			if time.Since(alive) > 5*time.Second {
				alive = time.Now()
				progress(n)
			}
			if l.msg == nil {
				n.Skipped++
				return nil
			}
			name, err := t.folder(c, l.path)
			if err != nil {
				return err
			}
			n.Folders = len(t.made)
			if !e.Dupes {
				ids, err := t.ids(c, name)
				if err != nil {
					return err
				}
				id := letterID(l.msg)
				if id != "" && ids[id] {
					n.Same++
					return nil
				}
				if id != "" {
					ids[id] = true
				}
			}
			if err := c.Append(name, l.flags, l.date, bytes.NewReader(l.msg)); err != nil {
				if c.State() == imap.LogoutState { // the connection is gone: the import stops
					return serverErr(err)
				}
				n.Skipped++ // the server refused this letter (too big, say): the others go on
				return nil
			}
			n.Letters++
			return nil
		})
		if err != nil {
			return err
		}
		// the new folders and the letters show in go24 at once
		if err := s.syncFolders(ctx, c, e.MailboxID); err != nil {
			return err
		}
		for _, name := range t.touched {
			var f Folder
			if err := s.db.QueryRow(ctx, `SELECT id::text, mailbox_id::text, name, delim, role, selectable, local FROM mail_folders
				WHERE mailbox_id::text = $1 AND name = $2`, e.MailboxID, name).
				Scan(&f.ID, &f.MailboxID, &f.Name, &f.Delim, &f.Role, &f.Selectable, &f.Local); err != nil {
				continue
			}
			if err := s.syncFolder(ctx, c, a, f); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// importTarget finds and makes the folders of the mailbox the letters go to.
type importTarget struct {
	delim   string
	into    Folder            // the chosen folder (no ID — the same folders)
	roles   map[string]string // the server names of the role folders
	exists  map[string]bool   // lower-case server names of the folders
	paths   map[string]string // a path of the file → the server name
	made    map[string]bool
	seen    map[string]map[string]bool // the Message-IDs of a folder
	touched []string
	tr      func(key string, args ...any) string
}

func (s *Store) newImportTarget(ctx context.Context, c *client.Client, mailboxID string, into Folder) (*importTarget, error) {
	t := &importTarget{delim: "/", into: into, roles: map[string]string{}, exists: map[string]bool{}, paths: map[string]string{},
		made: map[string]bool{}, seen: map[string]map[string]bool{}}
	ch := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- c.List("", "*", ch) }()
	for m := range ch {
		t.exists[strings.ToLower(m.Name)] = true
		if m.Delimiter != "" {
			t.delim = m.Delimiter
		}
	}
	if err := <-done; err != nil {
		return nil, serverErr(err)
	}
	rows, err := s.db.Query(ctx, `SELECT role, name FROM mail_folders WHERE mailbox_id::text = $1 AND role <> '' AND NOT local`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var role, name string
		if err := rows.Scan(&role, &name); err != nil {
			return nil, err
		}
		t.roles[role] = name
	}
	if t.roles["inbox"] == "" {
		t.roles["inbox"] = "INBOX"
	}
	return t, rows.Err()
}

// folder is the server name of the folder for a path of the file, made when missing: with the same folders, the
// path's first folder of a role is the mailbox's folder of it («Входящие» of a .pst — INBOX), the rest go at the top;
// into a chosen folder, the whole path goes under it. Letters at the file's top go to «Входящие» or the chosen folder.
func (t *importTarget) folder(c *client.Client, p []string) (string, error) {
	key := strings.Join(p, "\x00")
	if name, ok := t.paths[key]; ok {
		return name, nil
	}
	name, rest := t.into.Name, p
	if t.into.ID == "" {
		name = t.roles["inbox"]
		if len(p) > 0 {
			if role := importRole(p[0]); role != "" && t.roles[role] != "" {
				name, rest = t.roles[role], p[1:]
			} else {
				name = ""
			}
		}
	}
	for _, part := range rest {
		if strings.EqualFold(part, "INBOX") && t.tr != nil { // the inbox of another mailbox, under a folder
			part = t.tr("mail.folder.inbox")
		}
		part = t.safe(part)
		if name == "" {
			name = part
		} else {
			name += t.delim + part
		}
		if t.exists[strings.ToLower(name)] {
			continue
		}
		if err := c.Create(name); err != nil {
			return "", serverErr(err)
		}
		t.exists[strings.ToLower(name)] = true
		t.made[name] = true
	}
	t.paths[key] = name
	if !slices.Contains(t.touched, name) {
		t.touched = append(t.touched, name)
	}
	return name, nil
}

// safe makes a name of the file a folder name the server takes: no delimiter, no wildcard, not too long.
func (t *importTarget) safe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == '*' || r == '%' || strings.ContainsRune(t.delim, r) {
			return '_'
		}
		return r
	}, s)
	if r := []rune(strings.TrimSpace(s)); len(r) > MaxFolderName {
		s = string(r[:MaxFolderName])
	}
	if s = strings.TrimSpace(s); s == "" || strings.EqualFold(s, "INBOX") {
		s = "_" + s
	}
	return s
}

// ids are the Message-IDs of the letters of a folder, brought from the server once.
func (t *importTarget) ids(c *client.Client, name string) (map[string]bool, error) {
	if ids, ok := t.seen[name]; ok {
		return ids, nil
	}
	ids := map[string]bool{}
	t.seen[name] = ids
	if t.made[name] {
		return ids, nil
	}
	mb, err := c.Select(name, true)
	if err != nil {
		return nil, serverErr(err)
	}
	if mb.Messages == 0 {
		return ids, nil
	}
	set := new(imap.SeqSet)
	set.AddRange(1, 0)
	msgs := make(chan *imap.Message, 64)
	done := make(chan error, 1)
	go func() { done <- c.Fetch(set, []imap.FetchItem{imap.FetchEnvelope}, msgs) }()
	for m := range msgs {
		if m.Envelope != nil {
			if id := msgID(m.Envelope.MessageId); id != "" {
				ids[id] = true
			}
		}
	}
	if err := <-done; err != nil {
		return nil, serverErr(err)
	}
	return ids, nil
}

// importRole is the role of a folder of the file by its name ("" — none): the usual names, Outlook's own among them.
func importRole(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "inbox" || n == "входящие" {
		return "inbox"
	}
	for _, role := range Roles {
		for _, x := range roleNames[role] {
			if n == x && !(role == "sent" && n == "исходящие") { // Outlook's «Исходящие» is its Outbox
				return role
			}
		}
	}
	return ""
}

// letterID is the letter's Message-ID ("" — none).
func letterID(msg []byte) string {
	end := bytes.Index(msg, []byte("\r\n\r\n"))
	if end < 0 {
		end = len(msg)
	}
	for _, line := range strings.Split(string(msg[:end]), "\r\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Message-Id") {
			return msgID(v)
		}
	}
	return ""
}

// fileLetters reads the letters of the file one by one, in an order that is the same each time.
func fileLetters(format string, f *os.File, host string, each func(fileLetter) error) error {
	switch format {
	case "pst":
		return pstLetters(f, host, each)
	case "zip":
		return zipLetters(f, host, each)
	case "mbox":
		return mboxLetters(f, nil, host, each)
	case "eml":
		data, err := io.ReadAll(io.LimitReader(f, MaxSendSize*2+1))
		if err != nil {
			return err
		}
		return each(emlLetter(nil, data, host))
	}
	return i18n.E("err.mail_import_format")
}

// emlLetter is a letter of an .eml or .mbox, read (as Outlook marks imported letters) unless its «Status» or
// «X-Mozilla-Status» header (Thunderbird, mutt) says it was not.
func emlLetter(dir []string, data []byte, host string) fileLetter {
	msg, date, ok := letterOf(ImportFile{Name: "letter.eml", Data: data}, host)
	if !ok {
		return fileLetter{path: dir}
	}
	l := fileLetter{path: dir, msg: msg, date: date, flags: []string{imap.SeenFlag}}
	end := bytes.Index(msg, []byte("\r\n\r\n"))
	if end < 0 {
		return l
	}
	var status, moz string
	for _, line := range strings.Split(string(msg[:end]), "\r\n") {
		k, v, _ := strings.Cut(line, ":")
		switch strings.ToLower(k) {
		case "status":
			status = strings.TrimSpace(v)
		case "x-mozilla-status":
			moz = strings.TrimSpace(v)
		}
	}
	var mf int
	if moz != "" {
		if _, err := fmt.Sscanf(moz, "%x", &mf); err != nil {
			moz = ""
		}
	}
	switch {
	case moz != "":
		l.flags = nil
		if mf&0x1 != 0 {
			l.flags = append(l.flags, imap.SeenFlag)
		}
		if mf&0x2 != 0 {
			l.flags = append(l.flags, imap.AnsweredFlag)
		}
		if mf&0x4 != 0 {
			l.flags = append(l.flags, imap.FlaggedFlag)
		}
		if mf&0x1000 != 0 {
			l.flags = append(l.flags, "$Forwarded")
		}
	case status != "":
		l.flags = nil
		if strings.Contains(status, "R") {
			l.flags = append(l.flags, imap.SeenFlag)
		}
	}
	return l
}

// mboxLetters reads the letters of an mbox: each starts at a «From » line; a quoted «>From » line (mboxrd, mboxo)
// loses one «>».
func mboxLetters(r io.Reader, dir []string, host string, each func(fileLetter) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var cur bytes.Buffer
	in, big := false, false
	flush := func() error {
		if !in {
			return nil
		}
		defer cur.Reset()
		if big {
			big = false
			return each(fileLetter{path: dir})
		}
		return each(emlLetter(dir, cur.Bytes(), host))
	}
	start := true
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			if start && bytes.HasPrefix(line, []byte("From ")) {
				if err := flush(); err != nil {
					return err
				}
				in = true
			} else if in && !big {
				if start && bytes.HasPrefix(bytes.TrimLeft(line, ">"), []byte("From ")) {
					line = line[1:]
				}
				cur.Write(line)
				if cur.Len() > MaxSendSize*2 {
					big = true // too big for a letter: left out, read through
					cur.Reset()
				}
			}
			start = line[len(line)-1] == '\n'
		}
		if err == io.EOF {
			break
		}
		if err != nil && err != bufio.ErrBufferFull {
			return err
		}
	}
	return flush()
}

// zipLetters reads the letters of a .zip: its .mbox files (a folder each, by its path) and its .eml files (letters
// of the folder of their directory), in the order of their names.
func zipLetters(f *os.File, host string, each func(fileLetter) error) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	z, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return i18n.E("err.mail_import_bad")
	}
	files := append([]*zip.File{}, z.File...)
	sort.SliceStable(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for _, zf := range files {
		name := strings.ReplaceAll(zf.Name, "\\", "/")
		if zf.FileInfo().IsDir() || strings.HasPrefix(path.Base(name), ".") || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		ext := strings.ToLower(path.Ext(name))
		if ext != ".mbox" && ext != ".eml" && ext != ".mbx" {
			continue
		}
		var dir []string
		folder := strings.TrimSuffix(name, path.Ext(name))
		if ext == ".eml" {
			folder = path.Dir(name)
		}
		if folder != "." && folder != "" {
			dir = strings.Split(strings.Trim(folder, "/"), "/")
		}
		r, err := zf.Open()
		if err != nil {
			return i18n.E("err.mail_import_bad")
		}
		if ext == ".eml" {
			if zf.UncompressedSize64 > MaxSendSize*2 {
				err = each(fileLetter{path: dir})
			} else {
				var data []byte
				if data, err = io.ReadAll(r); err == nil {
					err = each(emlLetter(dir, data, host))
				}
			}
		} else {
			err = mboxLetters(r, dir, host, each)
		}
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// pstLetters reads the letters of Outlook's personal folders, folder by folder in the order of the tree; the file's
// top folder («Top of Personal Folders», «Начало файла данных Outlook») is not a folder of the path.
func pstLetters(f *os.File, host string, each func(fileLetter) error) error {
	head := make([]byte, 12)
	if _, err := f.ReadAt(head, 0); err == nil && string(head[:4]) == "!BDN" && head[10] < 23 {
		return i18n.E("err.mail_import_ansi") // Outlook 97–2002
	}
	p, err := pst.Open(f)
	if err != nil {
		return i18n.E("err.mail_import_bad")
	}
	tree, err := p.Tree()
	if err != nil {
		return i18n.E("err.mail_import_bad")
	}
	for _, folder := range tree {
		dir := folder.Path[1:]
		for _, nid := range folder.Letters {
			l, err := p.Letter(nid)
			if err != nil {
				if err := each(fileLetter{path: dir}); err != nil {
					return err
				}
				continue
			}
			if err := each(pstFileLetter(dir, l, host)); err != nil {
				return err
			}
		}
	}
	return nil
}

// pstFileLetter is a letter of a .pst as a mail server keeps it, with its flags; contacts, meetings, tasks, notes
// and the like are no letters.
func pstFileLetter(dir []string, l *pst.Letter, host string) fileLetter {
	class := strings.ToUpper(l.Props.String(pst.TagMessageClass))
	if class != "" && !strings.HasPrefix(class, "IPM.NOTE") && !strings.HasPrefix(class, "IPM.SCHEDULE.MEETING") &&
		!strings.HasPrefix(class, "REPORT.") && !strings.HasPrefix(class, "IPM.POST") {
		return fileLetter{path: dir}
	}
	props := func(p pst.Props) mapiProps {
		m := mapiProps{str: map[uint16]string{}, bin: map[uint16][]byte{}, num: map[uint16]uint64{}}
		for tag, v := range p {
			id := uint16(tag >> 16)
			switch tag & 0xFFFF {
			case 0x001F:
				m.str[id] = strings.TrimRight(p.String(tag), "\x00")
			case 0x001E:
				m.str[id] = strings.TrimRight(string(v), "\x00")
			case 0x0102:
				m.bin[id] = v
			case 0x0002, 0x0003, 0x000B, 0x0040, 0x0014:
				m.num[id] = uint64(p.Int(tag))
			}
		}
		return m
	}
	root := props(l.Props)
	root.str[prSubject] = l.Props.Subject()
	var recips, atts []mapiProps
	for _, r := range l.Recipients {
		recips = append(recips, props(r))
	}
	for _, a := range l.Attachments {
		atts = append(atts, props(a))
	}
	msg, date, ok := letterOf(ImportFile{Name: "letter.eml", Data: mapiMIME(root, recips, atts, host)}, host)
	if !ok {
		return fileLetter{path: dir}
	}
	if t := fileTime(root.num[prDeliveryTime]); !t.IsZero() {
		date = t
	}
	out := fileLetter{path: dir, msg: msg, date: date}
	if l.Props.Int(pst.TagMessageFlags)&1 != 0 { // mfRead
		out.flags = append(out.flags, imap.SeenFlag)
	}
	if l.Props.Int(pst.TagFlagStatus) == 2 { // followupFlagged
		out.flags = append(out.flags, imap.FlaggedFlag)
	}
	switch l.Props.Int(pst.TagLastVerbExecuted) {
	case 102, 103: // EXCHIVERB_REPLYTOSENDER, _REPLYTOALL
		out.flags = append(out.flags, imap.AnsweredFlag)
	case 104: // EXCHIVERB_FORWARD
		out.flags = append(out.flags, "$Forwarded")
	}
	return out
}
