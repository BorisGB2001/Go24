package mail

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/pst"
)

// «Экспорт», as Outlook's «Импорт и экспорт → Экспорт в файл»: a mailbox, its folders (with subfolders) and a period
// go to a file the user can open in another mail program — Outlook's personal folders (.pst, see exportpst.go),
// .mbox (mboxrd, a file per folder, as Thunderbird and Apple Mail keep mail) or .eml files by folders, the last two in
// a .zip. go24 worker makes it: the letters are brought from
// the server in batches and written into the archive as they come, so a big mailbox never sits in memory; the file
// goes on the user's drive and the user is notified. go24's own folders and letters, not on the server, are left out.

// ExportFormats are the formats of «Экспорт».
var ExportFormats = []string{"pst", "mbox", "eml"}

// MaxExportsQueued bounds the exports of a user waiting or running at once.
const MaxExportsQueued = 3

// exportBatch is how many letters one FETCH brings.
const exportBatch = 50

// Export is an export of letters to a file.
type Export struct {
	ID, UserID, MailboxID, Email string
	Folders                      []string // none: the whole mailbox
	Subfolders                   bool
	Since, Until                 *time.Time
	Format, Lang, Status         string
	Letters                      int
	Size                         int64
	FileID, URL, Error           string
	CreatedAt, UpdatedAt         time.Time
	FinishedAt                   *time.Time
	Tr                           func(key string, args ...any) string // the names of the role folders in a .pst
}

// Busy: the export waits or runs.
func (e Export) Busy() bool { return e.Status == "queued" || e.Status == "running" }

// NewExport is what the user asks to export.
type NewExport struct {
	MailboxID    string
	Folders      []string
	Subfolders   bool
	Since, Until *time.Time
	Format, Lang string
}

const exportSQL = `SELECT e.id::text, e.user_id::text, e.mailbox_id::text, m.email, e.folders::text[], e.subfolders, e.since, e.until,
		e.format, e.lang, e.status, e.letters, e.size, coalesce(e.file_id::text, ''), e.url, e.error, e.created_at, e.updated_at, e.finished_at
	FROM mail_exports e JOIN mailboxes m ON m.id = e.mailbox_id`

func scanExport(row pgx.Row) (Export, error) {
	var e Export
	err := row.Scan(&e.ID, &e.UserID, &e.MailboxID, &e.Email, &e.Folders, &e.Subfolders, &e.Since, &e.Until, &e.Format, &e.Lang,
		&e.Status, &e.Letters, &e.Size, &e.FileID, &e.URL, &e.Error, &e.CreatedAt, &e.UpdatedAt, &e.FinishedAt)
	return e, err
}

// AddExport puts an export in the queue of go24 worker.
func (s *Store) AddExport(ctx context.Context, userID string, n NewExport) (string, error) {
	if _, err := s.Mailbox(ctx, userID, n.MailboxID); err != nil {
		return "", err
	}
	if !validExportFormat(n.Format) {
		return "", i18n.E("err.mail_export_format")
	}
	if n.Since != nil && n.Until != nil && n.Until.Before(*n.Since) {
		return "", i18n.E("err.mail_export_period")
	}
	folders := []string{}
	for _, id := range n.Folders {
		f, err := s.Folder(ctx, userID, id)
		if err != nil {
			return "", err
		}
		if f.MailboxID != n.MailboxID {
			return "", ErrNotFound
		}
		if !f.Local {
			folders = append(folders, f.ID)
		}
	}
	if len(n.Folders) > 0 && len(folders) == 0 {
		return "", i18n.E("err.mail_export_local")
	}
	var busy int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM mail_exports WHERE user_id = $1 AND status IN ('queued', 'running')`,
		userID).Scan(&busy); err != nil {
		return "", err
	}
	if busy >= MaxExportsQueued {
		return "", i18n.E("err.mail_export_busy", MaxExportsQueued)
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO mail_exports (user_id, mailbox_id, folders, subfolders, since, until, format, lang)
		VALUES ($1, $2, $3::uuid[], $4, $5, $6, $7, $8) RETURNING id::text`,
		userID, n.MailboxID, folders, n.Subfolders, n.Since, n.Until, n.Format, n.Lang).Scan(&id)
	return id, err
}

func validExportFormat(f string) bool {
	for _, x := range ExportFormats {
		if x == f {
			return true
		}
	}
	return false
}

// Exports are the user's exports, the latest first.
func (s *Store) Exports(ctx context.Context, userID string) ([]Export, error) {
	rows, err := s.db.Query(ctx, exportSQL+` WHERE e.user_id::text = $1 ORDER BY e.created_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Export, error) { return scanExport(r) })
}

// GetExport is one export of the user.
func (s *Store) GetExport(ctx context.Context, userID, id string) (Export, error) {
	e, err := scanExport(s.db.QueryRow(ctx, exportSQL+` WHERE e.id::text = $1 AND e.user_id::text = $2`, id, userID))
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return e, err
}

// DeleteExport takes an export off the list (a waiting one is not made); the file stays on the drive.
func (s *Store) DeleteExport(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_exports WHERE id::text = $1 AND user_id::text = $2 AND status <> 'running'`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// exportStale: a running export not heard of so long is taken again (its worker stopped).
const exportStale = 10 * time.Minute

// ClaimExport takes the next export of the queue to make (ok false — none).
func (s *Store) ClaimExport(ctx context.Context) (e Export, ok bool, err error) {
	var id string
	err = s.db.QueryRow(ctx, `UPDATE mail_exports SET status = 'running', letters = 0, updated_at = now()
		WHERE id = (SELECT id FROM mail_exports WHERE status = 'queued' OR (status = 'running' AND updated_at < now() - $1::interval)
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id::text`, fmt.Sprintf("%d seconds", int(exportStale.Seconds()))).Scan(&id)
	if err == pgx.ErrNoRows {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	e, err = scanExport(s.db.QueryRow(ctx, exportSQL+` WHERE e.id::text = $1`, id))
	return e, err == nil, err
}

// FinishExport records how the export ended: its file on the drive and where it is, or the error.
func (s *Store) FinishExport(ctx context.Context, id string, letters int, size int64, fileID, url, errText string) error {
	status := "done"
	if errText != "" {
		status = "failed"
	}
	_, err := s.db.Exec(ctx, `UPDATE mail_exports SET status = $2, letters = $3, size = $4, file_id = nullif($5, '')::uuid, url = $6,
		error = left($7, 500), updated_at = now(), finished_at = now() WHERE id::text = $1`, id, status, letters, size, fileID, url, errText)
	return err
}

// exportFolders are the server folders the export takes, in the order of the folder list.
func (s *Store) exportFolders(ctx context.Context, e Export) ([]Folder, error) {
	all, err := s.Folders(ctx, e.UserID, e.MailboxID)
	if err != nil {
		return nil, err
	}
	chosen := map[string]bool{}
	for _, id := range e.Folders {
		chosen[id] = true
	}
	var picked []Folder
	for _, f := range all {
		if chosen[f.ID] {
			picked = append(picked, f)
		}
	}
	var out []Folder
	for _, f := range all {
		if !f.Selectable || f.Local {
			continue
		}
		take := len(e.Folders) == 0 || chosen[f.ID]
		for _, p := range picked {
			if e.Subfolders && p.Delim != "" && strings.HasPrefix(f.Name, p.Name+p.Delim) {
				take = true
			}
		}
		if take {
			out = append(out, f)
		}
	}
	return out, nil
}

// WriteExport writes the export's letters into w — a .pst (w is a file then), or a .zip — telling progress the letters
// done so far (the export is marked alive with it). It returns how many letters went.
func (s *Store) WriteExport(ctx context.Context, e Export, w io.Writer, progress func(int)) (int, error) {
	folders, err := s.exportFolders(ctx, e)
	if err != nil {
		return 0, err
	}
	if len(folders) == 0 {
		return 0, i18n.E("err.mail_export_local")
	}
	n := 0
	alive := time.Now()
	done := func() {
		n++
		if progress != nil && time.Since(alive) > 5*time.Second {
			alive = time.Now()
			progress(n)
		}
	}
	if e.Format == "pst" {
		out, ok := w.(pst.Output)
		if !ok {
			return 0, errNoPSTOutput
		}
		return n, s.writePST(ctx, e, folders, out, done)
	}
	z := zip.NewWriter(w)
	err = s.remote(ctx, e.MailboxID, func(c *client.Client, _ account) error {
		for _, f := range folders {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.exportFolder(c, z, e, f, done); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, z.Close()
}

// Progress records how many letters the running export wrote (and that it is alive).
func (s *Store) Progress(ctx context.Context, id string, letters int) error {
	_, err := s.db.Exec(ctx, `UPDATE mail_exports SET letters = $2, updated_at = now() WHERE id::text = $1 AND status = 'running'`, id, letters)
	return err
}

// exportFolder writes the letters of one folder: a .mbox file, or a folder of .eml files.
func (s *Store) exportFolder(c *client.Client, z *zip.Writer, e Export, f Folder, done func()) error {
	dir := exportPath(f)
	var mbox *bufio.Writer
	if e.Format == "mbox" {
		w, err := z.CreateHeader(&zip.FileHeader{Name: dir + ".mbox", Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		mbox = bufio.NewWriter(w)
	}
	num := 0
	err := s.eachLetter(c, e, f, func(m *imap.Message, r io.Reader) error {
		num++
		var err error
		if mbox != nil {
			err = writeMboxrd(mbox, m, r)
		} else {
			err = writeEML(z, dir, num, m, r)
		}
		if err == nil {
			done()
		}
		return err
	})
	if err != nil {
		return err
	}
	if mbox != nil {
		return mbox.Flush()
	}
	if num == 0 { // the empty folder is kept in the archive
		_, err = z.Create(dir + "/")
	}
	return err
}

// eachLetter gives the letters of a folder of the export's period, whole, as they come from the server in batches.
func (s *Store) eachLetter(c *client.Client, e Export, f Folder, each func(m *imap.Message, r io.Reader) error) error {
	if _, err := c.Select(f.Name, true); err != nil {
		return serverErr(err)
	}
	crit := imap.NewSearchCriteria()
	if e.Since != nil {
		crit.Since = *e.Since
	}
	if e.Until != nil {
		crit.Before = e.Until.AddDate(0, 0, 1)
	}
	uids, err := c.UidSearch(crit)
	if err != nil {
		return serverErr(err)
	}
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchInternalDate, imap.FetchEnvelope, imap.FetchFlags, section.FetchItem()}
	for start := 0; start < len(uids); start += exportBatch {
		set := new(imap.SeqSet)
		set.AddNum(uids[start:min(start+exportBatch, len(uids))]...)
		msgs := make(chan *imap.Message, exportBatch)
		fetched := make(chan error, 1)
		go func() { fetched <- c.UidFetch(set, items, msgs) }()
		var werr error
		for m := range msgs {
			r := m.GetBody(section)
			if r == nil || werr != nil {
				continue
			}
			werr = each(m, r)
		}
		if err := <-fetched; err != nil {
			return serverErr(err)
		}
		if werr != nil {
			return werr
		}
	}
	return nil
}

// exportPath is the folder's path in the archive: its parts by the server's delimiter, each safe as a file name.
func exportPath(f Folder) string {
	parts := []string{f.Name}
	if f.Delim != "" {
		parts = strings.Split(f.Name, f.Delim)
	}
	for i, p := range parts {
		parts[i] = safeName(p, 80)
	}
	return strings.Join(parts, "/")
}

// safeName makes a part of a path of an archive: no separators, no characters Windows forbids, at most max runes.
func safeName(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(strings.Trim(s, "."))
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max]))
	}
	if s == "" {
		s = "_"
	}
	return s
}

// writeEML writes a letter as its own .eml file of the folder: the number keeps the order and the names apart.
func writeEML(z *zip.Writer, dir string, num int, m *imap.Message, r io.Reader) error {
	subject := ""
	if m.Envelope != nil {
		subject = m.Envelope.Subject
	}
	name := fmt.Sprintf("%s/%05d", dir, num)
	if s := safeName(subject, 60); s != "_" {
		name += " " + s
	}
	w, err := z.CreateHeader(&zip.FileHeader{Name: name + ".eml", Method: zip.Deflate, Modified: m.InternalDate})
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return err
}

// writeMboxrd writes a letter into an mbox file in its mboxrd form: the «From » line of the sender and the date,
// the letter with its lines ending in LF, a «From » at a line's start (after any «>») quoted with one more «>», an
// empty line after.
func writeMboxrd(w *bufio.Writer, m *imap.Message, r io.Reader) error {
	from := "MAILER-DAEMON"
	if m.Envelope != nil && len(m.Envelope.From) > 0 {
		if a := m.Envelope.From[0].Address(); a != "" && !strings.ContainsAny(a, " \t") {
			from = a
		}
	}
	date := m.InternalDate
	if date.IsZero() {
		date = time.Now()
	}
	if _, err := fmt.Fprintf(w, "From %s %s\n", from, date.UTC().Format("Mon Jan _2 15:04:05 2006")); err != nil {
		return err
	}
	return mboxrdBody(w, r)
}

// mboxrdBody writes the letter's lines for an mbox file; a line longer than the buffer goes in parts, only its start
// is looked at.
func mboxrdBody(w *bufio.Writer, r io.Reader) error {
	br := bufio.NewReaderSize(r, 64<<10)
	start := true
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			whole := chunk[len(chunk)-1] == '\n'
			if whole {
				chunk = bytes.TrimSuffix(chunk[:len(chunk)-1], []byte("\r"))
			}
			if start && isFromLine(chunk) {
				if err := w.WriteByte('>'); err != nil {
					return err
				}
			}
			if _, err := w.Write(chunk); err != nil {
				return err
			}
			if whole {
				if err := w.WriteByte('\n'); err != nil {
					return err
				}
			}
			start = whole
		}
		if err == io.EOF {
			break
		}
		if err != nil && err != bufio.ErrBufferFull {
			return err
		}
	}
	if !start {
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.WriteByte('\n')
}

// isFromLine: the line is «From » after any number of «>» — mboxrd quotes it.
func isFromLine(b []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(b, ">"), []byte("From "))
}
