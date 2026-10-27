package mail

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/responses"

	"go24/internal/i18n"
)

// «Хранилище», as Outlook's «Параметры → Хранилище»: how much of the mailbox is taken and how much there is (the
// server's QUOTA), what each folder holds, and «Очистить» a folder — all its letters or those older than 3, 6 or 12
// months. Everything is counted on the server: go24 keeps only the newest letters of a folder.

// CleanMonths are the ages «Очистить» offers; 0 is all the letters.
var CleanMonths = []int{0, 3, 6, 12}

// Storage is a mailbox's «Хранилище».
type Storage struct {
	Used, Limit int64 // bytes, by the server's quota; Limit is 0 when the server tells none
	Total       int64 // the folders' letters together
	Folders     []FolderSize
}

// Percent is how full the mailbox is, of its quota.
func (st Storage) Percent() int {
	if st.Limit <= 0 {
		return 0
	}
	return int(min(100, st.Used*100/st.Limit))
}

// FolderSize is a folder of «Хранилище»: its letters on the server and their size.
type FolderSize struct {
	Folder
	Letters int
	Size    int64
}

// Storage counts the user's mailbox on its server, the biggest folders first.
func (s *Store) Storage(ctx context.Context, userID, mailboxID string) (Storage, error) {
	var st Storage
	if _, err := s.Mailbox(ctx, userID, mailboxID); err != nil {
		return st, err
	}
	folders, err := s.Folders(ctx, userID, mailboxID)
	if err != nil {
		return st, err
	}
	err = s.remote(ctx, mailboxID, func(c *client.Client, a account) error {
		if ok, _ := c.Support("QUOTA"); ok {
			st.Used, st.Limit = quotaOf(c)
		}
		sized, _ := c.Support("STATUS=SIZE")
		for _, f := range folders {
			if !f.Selectable || f.Local {
				continue
			}
			fs, err := folderSize(c, f, sized)
			if err != nil {
				return err
			}
			st.Folders = append(st.Folders, fs)
			st.Total += fs.Size
		}
		return nil
	})
	if err != nil {
		return st, serverErr(err)
	}
	if st.Limit == 0 && st.Used == 0 {
		st.Used = st.Total
	}
	slices.SortStableFunc(st.Folders, func(a, b FolderSize) int { return cmp.Compare(b.Size, a.Size) }) // the biggest first
	return st, nil
}

// folderSize: STATUS with SIZE (RFC 8438) when the server has it, else the sizes of the folder's letters.
func folderSize(c *client.Client, f Folder, sized bool) (FolderSize, error) {
	fs := FolderSize{Folder: f}
	if sized {
		status, err := c.Status(f.Name, []imap.StatusItem{imap.StatusMessages, "SIZE"})
		if err != nil {
			return fs, err
		}
		fs.Letters = int(status.Messages)
		if n, err := imap.ParseNumber(status.Items["SIZE"]); err == nil {
			fs.Size = int64(n)
			return fs, nil
		}
		// SIZE is a 63-bit number: one too big for ParseNumber is read as a string
		if v, ok := status.Items["SIZE"].(string); ok {
			var n int64
			for _, r := range v {
				n = n*10 + int64(r-'0')
			}
			fs.Size = n
		}
		return fs, nil
	}
	box, err := c.Select(f.Name, true)
	if err != nil {
		return fs, err
	}
	fs.Letters = int(box.Messages)
	if box.Messages == 0 {
		return fs, nil
	}
	set := new(imap.SeqSet)
	set.AddRange(1, box.Messages)
	msgs := make(chan *imap.Message, 64)
	done := make(chan error, 1)
	go func() { done <- c.Fetch(set, []imap.FetchItem{imap.FetchRFC822Size}, msgs) }()
	for m := range msgs {
		fs.Size += int64(m.Size)
	}
	return fs, <-done
}

// quotaCmd is GETQUOTAROOT (RFC 2087), which go-imap does not have.
type quotaCmd struct{ mailbox string }

func (q quotaCmd) Command() *imap.Command {
	return &imap.Command{Name: "GETQUOTAROOT", Arguments: []interface{}{imap.FormatMailboxName(q.mailbox)}}
}

// quotaOf: the STORAGE of the inbox's quota root, in bytes (the server counts in KiB); nothing when the server
// tells none.
func quotaOf(c *client.Client) (used, limit int64) {
	h := responses.HandlerFunc(func(resp imap.Resp) error {
		name, fields, ok := imap.ParseNamedResp(resp)
		if !ok || name != "QUOTA" || len(fields) < 2 || limit > 0 {
			return responses.ErrUnhandled
		}
		list, _ := fields[1].([]interface{})
		if u, l, ok := parseQuota(list); ok {
			used, limit = u, l
		}
		return nil
	})
	if status, err := c.Execute(quotaCmd{"INBOX"}, h); err != nil || status.Err() != nil {
		return 0, 0
	}
	return used, limit
}

// parseQuota: the STORAGE resource of a QUOTA response's list (name, usage, limit, …), from KiB.
func parseQuota(list []interface{}) (used, limit int64, ok bool) {
	for i := 0; i+2 < len(list); i += 3 {
		res, _ := list[i].(string)
		u, err1 := imap.ParseNumber(list[i+1])
		l, err2 := imap.ParseNumber(list[i+2])
		if strings.EqualFold(res, "STORAGE") && err1 == nil && err2 == nil {
			return int64(u) * 1024, int64(l) * 1024, true
		}
	}
	return 0, 0, false
}

// cleanBatch bounds the letters of one command of «Очистить».
const cleanBatch = 500

// Clean is «Очистить» of the user's folder: its letters older than the months (0 — all of them) go to «Удалённые»,
// from «Удалённые» and «Нежелательная почта» for good. Pinned letters stay. The number of letters gone is returned.
func (s *Store) Clean(ctx context.Context, userID, folderID string, months int) (int, error) {
	if !slices.Contains(CleanMonths, months) {
		return 0, i18n.E("err.bad_form")
	}
	f, err := s.Folder(ctx, userID, folderID)
	if err != nil {
		return 0, err
	}
	if !f.Selectable || f.Role == "drafts" {
		return 0, i18n.E("err.mail_clean_folder")
	}
	var before time.Time
	cond := `f.id::text = $1 AND m.user_id::text = $2 AND NOT x.pinned`
	args := []any{folderID, userID}
	if months > 0 {
		before = time.Now().AddDate(0, -months, 0)
		cond += ` AND x.sent_at < $3`
		args = append(args, before)
	}
	// go24's own letters (not on the server) go as «Удалить» takes them
	own, err := s.lettersWhere(ctx, cond+` AND (f.local OR x.uid > `+maxUIDText+`)`, args...)
	if err != nil {
		return 0, err
	}
	n := len(own)
	if len(own) > 0 {
		if err := s.file(ctx, userID, own, "delete"); err != nil {
			return 0, err
		}
	}
	if f.Local {
		return n, nil
	}
	forGood := f.Role == "trash" || f.Role == "junk"
	var trash Folder
	if !forGood {
		if trash, err = s.roleFolder(ctx, f.MailboxID, "trash"); err != nil {
			return n, err
		}
	}
	pinned := map[uint32]bool{}
	rows, err := s.db.Query(ctx, `SELECT uid FROM mail_messages WHERE folder_id::text = $1 AND pinned AND uid <= `+maxUIDText, folderID)
	if err != nil {
		return n, err
	}
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			rows.Close()
			return n, err
		}
		pinned[uint32(uid)] = true
	}
	rows.Close()
	var gone []int64
	err = s.remote(ctx, f.MailboxID, func(c *client.Client, a account) error {
		if _, err := c.Select(f.Name, false); err != nil {
			return err
		}
		crit := imap.NewSearchCriteria()
		if months > 0 {
			crit.SentBefore = before // by the letter's date, as the list shows it
		}
		uids, err := c.UidSearch(crit)
		if err != nil {
			return err
		}
		var take []uint32
		for _, u := range uids {
			if !pinned[u] {
				take = append(take, u)
			}
		}
		for i := 0; i < len(take); i += cleanBatch {
			part := take[i:min(i+cleanBatch, len(take))]
			set := new(imap.SeqSet)
			set.AddNum(part...)
			if forGood {
				if err := c.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil); err != nil {
					return err
				}
				if err := c.Expunge(nil); err != nil {
					return err
				}
			} else if err := c.UidMove(set, trash.Name); err != nil {
				return err
			}
			for _, u := range part {
				gone = append(gone, int64(u))
			}
		}
		if len(gone) == 0 || forGood {
			return nil
		}
		// a few letters come into «Удалённые» in go24 as moved ones do; a whole mailbox's old letters are not
		// downloaded: they are in «Удалённые» on the server
		if len(gone) <= firstFetch {
			return s.syncFolder(ctx, c, a, trash)
		}
		box, err := c.Status(trash.Name, []imap.StatusItem{imap.StatusUidNext, imap.StatusUidValidity})
		if err != nil || box.UidNext == 0 {
			return err
		}
		_, err = s.db.Exec(ctx, `UPDATE mail_folders SET last_uid = greatest(last_uid, $2) WHERE id::text = $1 AND validity = $3`,
			trash.ID, int64(box.UidNext-1), int64(box.UidValidity))
		return err
	})
	if len(gone) > 0 {
		if _, derr := s.db.Exec(context.WithoutCancel(ctx), `DELETE FROM mail_messages WHERE folder_id::text = $1 AND uid = ANY($2)`,
			folderID, gone); derr != nil && err == nil {
			err = derr
		}
		s.Changed(ctx, userID)
	}
	if err != nil {
		return n + len(gone), serverErr(err)
	}
	return n + len(gone), nil
}

const maxUIDText = "4294967295" // maxUID in SQL
