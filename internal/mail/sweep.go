package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go24/internal/i18n"
)

// «Очистка», as Outlook's Sweep, on the senders of the letters picked, in their folder: all their letters go now
// (all); and those to come (future); always all but the latest one (latest); always those older than N days (older).
// They go to «Удалённые» (delete), «Архив» (archive) or a folder of the mailbox. The standing ones are kept and the
// worker keeps doing them (SweepDue). Pinned letters stay.

// Sweep modes.
var SweepModes = []string{"all", "future", "latest", "older"}

// Sweep is a standing «Очистка» of the user.
type Sweep struct {
	ID, FolderID, FolderName, FolderRole, Sender, Mode, Target string
	Days                                                       int
}

// SweepNow does «Очистка» for the senders of the letters of the user; the number of letters gone.
func (s *Store) SweepNow(ctx context.Context, userID string, letterIDs []string, mode, target string, days int) (int, error) {
	if !oneOf(mode, SweepModes) {
		return 0, i18n.E("err.bad_form")
	}
	if mode != "older" {
		days = 10
	}
	if days < 1 || days > 3650 {
		return 0, i18n.E("err.mail_sweep_days")
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT x.folder_id::text || ' ' || lower(x.from_addr) FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = ANY($1) AND m.user_id::text = $2 AND x.from_addr <> ''`, letterIDs, userID)
	if err != nil {
		return 0, err
	}
	pairs, err := pgxCollectStrings(rows)
	if err != nil {
		return 0, err
	}
	if len(pairs) == 0 {
		return 0, ErrNotFound
	}
	n := 0
	for _, p := range pairs {
		folder, sender, _ := strings.Cut(p, " ")
		if err := s.checkTarget(ctx, userID, folder, target); err != nil {
			return n, err
		}
		standing := map[string]string{"future": "all", "latest": "latest", "older": "older"}[mode]
		if standing != "" {
			if _, err := s.db.Exec(ctx, `INSERT INTO mail_sweeps (user_id, folder_id, sender, mode, days, target) VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (folder_id, sender) DO UPDATE SET mode = EXCLUDED.mode, days = EXCLUDED.days, target = EXCLUDED.target`,
				userID, folder, sender, standing, days, target); err != nil {
				return n, err
			}
		} else {
			standing = "all"
		}
		k, err := s.sweep(ctx, userID, folder, sender, standing, days, target)
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// checkTarget: delete, archive, or another selectable folder of the folder's mailbox.
func (s *Store) checkTarget(ctx context.Context, userID, folderID, target string) error {
	if target == "delete" || target == "archive" {
		return nil
	}
	from, err := s.Folder(ctx, userID, folderID)
	if err != nil {
		return err
	}
	to, err := s.Folder(ctx, userID, target)
	if err != nil {
		return err
	}
	if to.MailboxID != from.MailboxID || to.ID == from.ID || !to.Selectable {
		return i18n.E("err.mail_move_box")
	}
	return nil
}

// sweep moves the sender's letters of the folder that the mode takes; how many.
func (s *Store) sweep(ctx context.Context, userID, folderID, sender, mode string, days int, target string) (int, error) {
	cond := map[string]string{
		"all": "true",
		"latest": `x.id <> (SELECT y.id FROM mail_messages y WHERE y.folder_id = x.folder_id AND lower(y.from_addr) = lower(x.from_addr)
			ORDER BY y.sent_at DESC, y.id DESC LIMIT 1)`,
		"older": fmt.Sprintf("x.sent_at < now() - interval '%d days'", days),
	}[mode]
	if cond == "" {
		return 0, i18n.E("err.bad_form")
	}
	list, err := s.lettersWhere(ctx, `f.id::text = $1 AND m.user_id::text = $2 AND lower(x.from_addr) = $3 AND NOT x.pinned
		AND NOT EXISTS (SELECT 1 FROM mail_drafts d WHERE d.id = x.id) AND `+cond, folderID, userID, sender)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	switch target {
	case "delete", "archive":
		err = s.file(ctx, userID, list, target)
	default:
		err = s.Move(ctx, userID, ids(list), target)
	}
	return len(list), err
}

// Sweeps are the user's standing «Очистка», by folder and sender.
func (s *Store) Sweeps(ctx context.Context, userID string) ([]Sweep, error) {
	rows, err := s.db.Query(ctx, `SELECT w.id::text, f.id::text, f.name, f.role, w.sender, w.mode, w.target, w.days FROM mail_sweeps w
		JOIN mail_folders f ON f.id = w.folder_id WHERE w.user_id::text = $1 ORDER BY f.name, w.sender`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sweep
	for rows.Next() {
		var w Sweep
		if err := rows.Scan(&w.ID, &w.FolderID, &w.FolderName, &w.FolderRole, &w.Sender, &w.Mode, &w.Target, &w.Days); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DropSweep stops a standing «Очистка» of the user.
func (s *Store) DropSweep(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_sweeps WHERE id::text = $1 AND user_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SweepDue does the standing «Очистка» of everyone (the worker calls it each minute).
func (s *Store) SweepDue(ctx context.Context) (errs []error) {
	rows, err := s.db.Query(ctx, `SELECT user_id::text, folder_id::text, sender, mode, days, target FROM mail_sweeps ORDER BY user_id`)
	if err != nil {
		return []error{err}
	}
	type job struct {
		user, folder, sender, mode, target string
		days                               int
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.user, &j.folder, &j.sender, &j.mode, &j.days, &j.target); err != nil {
			rows.Close()
			return []error{err}
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	for _, j := range jobs {
		if _, err := s.sweep(ctx, j.user, j.folder, j.sender, j.mode, j.days, j.target); err != nil && !errors.Is(err, ErrNotFound) {
			errs = append(errs, fmt.Errorf("sweep %s %s: %w", j.folder, j.sender, err))
		}
	}
	return errs
}

// SweepFrom is a sender of the letters picked for «Очистка» and their letters in the folder.
type SweepFrom struct {
	Sender, Name                                string
	FolderID, FolderName, FolderRole, MailboxID string
	Count                                       int
}

// SweepPreview: the senders of the letters of the user, each with its letters in the folder (as the «Очистка»
// dialog shows them).
func (s *Store) SweepPreview(ctx context.Context, userID string, letterIDs []string) ([]SweepFrom, error) {
	rows, err := s.db.Query(ctx, `SELECT lower(x.from_addr), max(x.from_name), f.id::text, f.name, f.role, f.mailbox_id::text,
			(SELECT count(*) FROM mail_messages y WHERE y.folder_id = f.id AND lower(y.from_addr) = lower(min(x.from_addr)))::int
		FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE x.id::text = ANY($1) AND m.user_id::text = $2 AND x.from_addr <> ''
		GROUP BY lower(x.from_addr), f.id ORDER BY 1`, letterIDs, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SweepFrom
	for rows.Next() {
		var w SweepFrom
		if err := rows.Scan(&w.Sender, &w.Name, &w.FolderID, &w.FolderName, &w.FolderRole, &w.MailboxID, &w.Count); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
