package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Назначить политику», as Outlook's retention policies: a folder and a letter may have one — a letter goes to
// «Удалённые» (del_*) or «Архив» (arch_*) when it is that old, «Никогда не удалять» keeps it. A letter's own policy
// is stronger than its folder's; without one the folder's holds. The worker carries out what is due (RetentionDue).

// Policy is a retention policy: what becomes of a letter and after how many days.
type Policy struct {
	Key, Action string // Action: delete, archive, "" — kept
	Days        int
}

// Policies are those one may assign, as Outlook offers them.
var Policies = []Policy{
	{"del_1w", "delete", 7}, {"del_1m", "delete", 30}, {"del_6m", "delete", 182}, {"del_1y", "delete", 365},
	{"del_5y", "delete", 1826}, {"arch_1y", "archive", 365}, {"arch_5y", "archive", 1826}, {"never", "", 0},
}

// PolicyOf is the policy by its key (false — none such).
func PolicyOf(key string) (Policy, bool) {
	for _, p := range Policies {
		if p.Key == key {
			return p, true
		}
	}
	return Policy{}, false
}

// checkPolicy: "" or a known policy.
func checkPolicy(key string) error {
	if _, ok := PolicyOf(key); key != "" && !ok {
		return i18n.E("err.bad_form")
	}
	return nil
}

// SetFolderPolicy assigns a folder of the user its policy ("" — none).
func (s *Store) SetFolderPolicy(ctx context.Context, userID, folderID, key string) error {
	if err := checkPolicy(key); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE mail_folders f SET retention = $3 FROM mailboxes m
		WHERE m.id = f.mailbox_id AND m.user_id::text = $1 AND f.id::text = $2`, userID, folderID, key)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SetLetterPolicy assigns letters of the user their own policy ("" — the folder's); how many.
func (s *Store) SetLetterPolicy(ctx context.Context, userID string, letterIDs []string, key string) (int, error) {
	if err := checkPolicy(key); err != nil {
		return 0, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE mail_messages x SET retention = $3 FROM mailboxes m
		WHERE m.id = x.mailbox_id AND m.user_id::text = $1 AND x.id::text = ANY($2)`, userID, letterIDs, key)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return int(tag.RowsAffected()), err
}

// Retention is a letter's policy as the reading pane shows it.
type Retention struct {
	Own, Folder string     // the letter's own policy, its folder's ("" — none)
	Expires     *time.Time // when the letter goes (nil — never)
}

// Policy is the policy that holds ("" — none).
func (r Retention) Policy() string {
	if r.Own != "" {
		return r.Own
	}
	return r.Folder
}

// Action is what becomes of the letter when it expires: delete, archive ("" — nothing).
func (r Retention) Action() string {
	p, _ := PolicyOf(r.Policy())
	return p.Action
}

// LetterRetention is the policy of a letter of the user.
func (s *Store) LetterRetention(ctx context.Context, userID, letterID string) (Retention, error) {
	var r Retention
	var sent time.Time
	err := s.db.QueryRow(ctx, `SELECT x.retention, f.retention, x.sent_at FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id
		JOIN mailboxes m ON m.id = f.mailbox_id WHERE m.user_id::text = $1 AND x.id::text = $2`, userID, letterID).Scan(&r.Own, &r.Folder, &sent)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	} else if err != nil {
		return r, err
	}
	if p, _ := PolicyOf(r.Policy()); p.Days > 0 {
		at := sent.AddDate(0, 0, p.Days)
		r.Expires = &at
	}
	return r, nil
}

// policyDays is a VALUES list of the policies that move letters: (key, action, days).
func policyDays() string {
	var b []string
	for _, p := range Policies {
		if p.Action != "" {
			b = append(b, fmt.Sprintf("('%s', '%s', %d)", p.Key, p.Action, p.Days))
		}
	}
	return strings.Join(b, ", ")
}

// RetentionDue carries out the policies of everyone: the letters that are old enough go to «Удалённые» or «Архив»
// (the worker calls it hourly); how many went. Drafts stay; archiving ones in «Архив» already does nothing; in
// «Удалённые» only the folder's own policy holds (a letter's «Удалить через…» brought it there — it is not erased).
func (s *Store) RetentionDue(ctx context.Context) (int, []error) {
	rows, err := s.db.Query(ctx, `WITH l AS (
			SELECT x.id, m.user_id, coalesce(nullif(x.retention, ''), f.retention) AS p, x.sent_at, f.role
			FROM mail_folders f JOIN mail_messages x ON x.folder_id = f.id JOIN mailboxes m ON m.id = f.mailbox_id WHERE f.retention <> ''
			UNION
			SELECT x.id, m.user_id, x.retention, x.sent_at, f.role
			FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
			WHERE x.retention <> '' AND f.role <> 'trash')
		SELECT l.user_id::text, v.action, array_agg(l.id::text) FROM l JOIN (VALUES `+policyDays()+`) v(p, action, days) ON v.p = l.p
		WHERE l.sent_at < now() - make_interval(days => v.days) AND NOT (v.action = 'archive' AND l.role = 'archive')
			AND NOT EXISTS (SELECT 1 FROM mail_drafts d WHERE d.id = l.id)
		GROUP BY l.user_id, v.action ORDER BY 1, 2`)
	if err != nil {
		return 0, []error{err}
	}
	type job struct {
		user, action string
		ids          []string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.user, &j.action, &j.ids); err != nil {
			rows.Close()
			return 0, []error{err}
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, []error{err}
	}
	n := 0
	var errs []error
	for _, j := range jobs {
		list, err := s.letters(ctx, j.user, j.ids)
		if err == nil {
			err = s.file(ctx, j.user, list, j.action)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("retention %s %s: %w", j.user, j.action, err))
			continue
		}
		n += len(list)
		s.Changed(ctx, j.user)
	}
	return n, errs
}
