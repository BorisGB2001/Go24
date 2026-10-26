package mail

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Игнорировать беседу», as Outlook's: the letters of the conversation go to «Удалённые», and so do those that come
// in it later (before the rules and the notifications). Ignoring a conversation ignored already stops it: its letters
// in «Удалённые» go back — those the mailbox sent to «Отправленные», the rest to «Входящие».
//
// «Быстрые действия» (Quick Steps), as Outlook's: the user's sets of actions done on the letters picked by one
// button. The letters get the marks first, then are forwarded, then filed (moved, archived, deleted, junk, ignored —
// one of them, the last thing done). «Ответить» opens the reply (with a template) — the web does that before the
// rest, as the letter may leave the folder. Outlook's «Готово» and «Ответить и удалить» are made the first time.

// ignoreKey is a letter's conversation in mail_ignored (its Message-ID keeps it when the letter moves).
const ignoreKey = `coalesce(nullif(x.thread, ''), nullif(x.message_id, ''))`

// Ignored tells whether the letter's conversation is ignored.
func (s *Store) Ignored(ctx context.Context, userID, letterID string) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		JOIN mail_ignored i ON i.mailbox_id = x.mailbox_id AND i.thread = `+ignoreKey+`
		WHERE x.id::text = $1 AND m.user_id::text = $2)`, letterID, userID).Scan(&on)
	return on, err
}

// ignore ignores the conversations of the letters (on) or stops ignoring them.
func (s *Store) ignore(ctx context.Context, userID string, all []string, on bool) error {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT x.mailbox_id::text, `+ignoreKey+` FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = ANY($1) AND m.user_id::text = $2 AND `+ignoreKey+` IS NOT NULL`, all, userID)
	if err != nil {
		return err
	}
	pairs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Box, Thread string }])
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if on {
			if _, err := s.db.Exec(ctx, `INSERT INTO mail_ignored (mailbox_id, thread) VALUES ($1, $2) ON CONFLICT DO NOTHING`, p.Box, p.Thread); err != nil {
				return err
			}
			list, err := s.lettersWhere(ctx, `x.mailbox_id::text = $1 AND `+ignoreKey+` = $2 AND f.role NOT IN ('trash', 'drafts')
				AND NOT EXISTS (SELECT 1 FROM mail_drafts d WHERE d.id = x.id)`, p.Box, p.Thread)
			if err == nil && len(list) > 0 {
				err = s.file(ctx, userID, list, "delete")
			}
			if err != nil {
				return err
			}
			continue
		}
		if _, err := s.db.Exec(ctx, `DELETE FROM mail_ignored WHERE mailbox_id::text = $1 AND thread = $2`, p.Box, p.Thread); err != nil {
			return err
		}
		for _, role := range []string{"sent", "inbox"} {
			sent := `lower(x.from_addr) = lower(m.email)`
			if role == "inbox" {
				sent = "NOT " + sent
			}
			list, err := s.lettersWhere(ctx, `x.mailbox_id::text = $1 AND `+ignoreKey+` = $2 AND f.role = 'trash' AND `+sent, p.Box, p.Thread)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				continue
			}
			to, err := s.folderByRole(ctx, p.Box, role)
			if err == nil && to.ID == "" {
				to, err = s.folderByRole(ctx, p.Box, "inbox")
			}
			if err == nil && to.ID != "" {
				err = s.Move(ctx, userID, ids(list), to.ID)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// trashIgnored: the new letters of ignored conversations go to «Удалённые»; the rest are returned.
func (s *Store) trashIgnored(ctx context.Context, userID string, news []arrived) ([]arrived, error) {
	all := make([]string, len(news))
	for i, n := range news {
		all[i] = n.id
	}
	rows, err := s.db.Query(ctx, `SELECT x.id::text FROM mail_messages x JOIN mail_ignored i ON i.mailbox_id = x.mailbox_id AND i.thread = `+
		ignoreKey+` WHERE x.id::text = ANY($1)`, all)
	if err != nil {
		return news, err
	}
	gone, err := pgxCollectStrings(rows)
	if err != nil || len(gone) == 0 {
		return news, err
	}
	var rest []arrived
	for _, n := range news {
		if !contains(gone, n.id) {
			rest = append(rest, n)
		}
	}
	list, err := s.letters(ctx, userID, gone)
	if err == nil {
		err = s.file(ctx, userID, list, "delete")
	}
	return rest, err
}

// Step is a step of a quick step: an action and what it takes — a folder (move), a category, addresses (forward),
// a template (reply, replyall: may be empty).
type Step struct {
	Do  string `json:"do"`
	Arg string `json:"arg,omitempty"`
}

// QuickStep is a «Быстрое действие» of the user.
type QuickStep struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Pos   int    `json:"pos"`
	Steps []Step `json:"steps"`
}

// StepActions are what a step does; the filing ones — at most one in a quick step, done last; the replies — at most
// one too.
var (
	StepActions = []string{"read", "unread", "flag", "done", "pin", "category", "forward", "reply", "replyall",
		"move", "archive", "delete", "junk", "ignore"}
	filing  = []string{"move", "archive", "delete", "junk", "ignore"}
	replies = []string{"reply", "replyall"}
)

// MaxSteps bounds the steps of a quick step; MaxQuickSteps — the user's quick steps.
const (
	MaxSteps      = 8
	MaxQuickSteps = 30
)

// Reply is the quick step's reply step, if it has one.
func (q QuickStep) Reply() (Step, bool) {
	for _, st := range q.Steps {
		if oneOf(st.Do, replies) {
			return st, true
		}
	}
	return Step{}, false
}

// Files tells whether the quick step takes the letters out of their folder.
func (q QuickStep) Files() bool {
	for _, st := range q.Steps {
		if oneOf(st.Do, filing) {
			return true
		}
	}
	return false
}

// checkQuickStep tidies a quick step and checks what its steps take belongs to the user.
func (s *Store) checkQuickStep(ctx context.Context, userID string, q *QuickStep) error {
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" || utf8.RuneCountInString(q.Name) > 60 {
		return i18n.E("err.mail_qs_name")
	}
	var steps []Step
	files, answers := 0, 0
	for _, st := range q.Steps {
		st.Arg = strings.TrimSpace(st.Arg)
		if st.Do == "" {
			continue
		}
		if !oneOf(st.Do, StepActions) {
			return i18n.E("err.bad_form")
		}
		switch st.Do {
		case "move":
			f, err := s.Folder(ctx, userID, st.Arg)
			if err != nil || !f.Selectable {
				return i18n.E("err.mail_qs_folder")
			}
		case "category":
			if _, err := s.Category(ctx, userID, st.Arg); err != nil {
				return i18n.E("err.mail_qs_category")
			}
		case "forward":
			list, err := addresses(st.Arg)
			if err != nil || len(list) == 0 {
				return i18n.E("err.mail_qs_forward")
			}
		case "reply", "replyall":
			if st.Arg != "" {
				if _, err := s.Snippet(ctx, userID, "template", st.Arg); err != nil {
					return i18n.E("err.mail_qs_template")
				}
			}
		default:
			st.Arg = ""
		}
		if oneOf(st.Do, filing) {
			files++
		}
		if oneOf(st.Do, replies) {
			answers++
		}
		steps = append(steps, st)
	}
	switch {
	case len(steps) == 0:
		return i18n.E("err.mail_qs_empty")
	case len(steps) > MaxSteps:
		return i18n.E("err.mail_qs_many")
	case files > 1:
		return i18n.E("err.mail_qs_files")
	case answers > 1:
		return i18n.E("err.mail_qs_replies")
	}
	q.Steps = steps
	return nil
}

func scanQuickSteps(rows pgx.Rows, err error) ([]QuickStep, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuickStep
	for rows.Next() {
		var q QuickStep
		var raw []byte
		if err := rows.Scan(&q.ID, &q.Name, &q.Pos, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &q.Steps); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

const quickStepSQL = `SELECT id::text, name, pos, steps FROM mail_quicksteps`

// QuickSteps are the user's quick steps, in their order; defaults (a name and steps) are made the first time.
func (s *Store) QuickSteps(ctx context.Context, userID string, defaults ...QuickStep) ([]QuickStep, error) {
	list, err := scanQuickSteps(s.db.Query(ctx, quickStepSQL+` WHERE user_id::text = $1 ORDER BY pos, name`, userID))
	if err != nil || len(list) > 0 || len(defaults) == 0 {
		return list, err
	}
	var has bool // made once: a user who deleted them all has none
	if err := s.db.QueryRow(ctx, `SELECT mail_quicksteps_made FROM users WHERE id::text = $1`, userID).Scan(&has); err != nil || has {
		return list, err
	}
	for i, d := range defaults {
		steps, _ := json.Marshal(d.Steps)
		if _, err := s.db.Exec(ctx, `INSERT INTO mail_quicksteps (user_id, name, pos, steps) VALUES ($1, $2, $3, $4)`, userID, d.Name, i, steps); err != nil {
			return nil, err
		}
	}
	if _, err := s.db.Exec(ctx, `UPDATE users SET mail_quicksteps_made = true WHERE id::text = $1`, userID); err != nil {
		return nil, err
	}
	return s.QuickSteps(ctx, userID)
}

// QuickStepOf is a quick step of the user.
func (s *Store) QuickStepOf(ctx context.Context, userID, id string) (QuickStep, error) {
	list, err := scanQuickSteps(s.db.Query(ctx, quickStepSQL+` WHERE id::text = $1 AND user_id::text = $2`, id, userID))
	if err != nil {
		return QuickStep{}, err
	}
	if len(list) == 0 {
		return QuickStep{}, ErrNotFound
	}
	return list[0], nil
}

// SaveQuickStep makes a quick step of the user (ID "": the last one) or changes one.
func (s *Store) SaveQuickStep(ctx context.Context, userID string, q QuickStep) (string, error) {
	if err := s.checkQuickStep(ctx, userID, &q); err != nil {
		return "", err
	}
	steps, err := json.Marshal(q.Steps)
	if err != nil {
		return "", err
	}
	if q.ID == "" {
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM mail_quicksteps WHERE user_id::text = $1`, userID).Scan(&n); err != nil {
			return "", err
		}
		if n >= MaxQuickSteps {
			return "", i18n.E("err.mail_qs_limit")
		}
		err = s.db.QueryRow(ctx, `INSERT INTO mail_quicksteps (user_id, name, pos, steps) VALUES ($1::uuid, $2,
			(SELECT coalesce(max(pos) + 1, 0) FROM mail_quicksteps WHERE user_id = $1::uuid), $3) RETURNING id::text`, userID, q.Name, steps).Scan(&q.ID)
		if err == nil {
			_, err = s.db.Exec(ctx, `UPDATE users SET mail_quicksteps_made = true WHERE id::text = $1`, userID)
		}
		return q.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE mail_quicksteps SET name = $3, steps = $4 WHERE id::text = $1 AND user_id::text = $2`, q.ID, userID, q.Name, steps)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return q.ID, err
}

// DeleteQuickStep deletes a quick step of the user.
func (s *Store) DeleteQuickStep(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_quicksteps WHERE id::text = $1 AND user_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// MoveQuickStep moves a quick step of the user up (d < 0) or down the list.
func (s *Store) MoveQuickStep(ctx context.Context, userID, id string, d int) error {
	list, err := s.QuickSteps(ctx, userID)
	if err != nil {
		return err
	}
	at := -1
	for i, q := range list {
		if q.ID == id {
			at = i
		}
	}
	if at < 0 {
		return ErrNotFound
	}
	to := at + d
	if to < 0 || to >= len(list) {
		return nil
	}
	list[at], list[to] = list[to], list[at]
	for i, q := range list {
		if _, err := s.db.Exec(ctx, `UPDATE mail_quicksteps SET pos = $2 WHERE id::text = $1`, q.ID, i); err != nil {
			return err
		}
	}
	return nil
}

// DoQuickStep does a quick step on letters of the user, but its reply (the web opens it).
func (s *Store) DoQuickStep(ctx context.Context, userID string, letterIDs []string, q QuickStep) error {
	list, err := s.letters(ctx, userID, letterIDs)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return ErrNotFound
	}
	all := ids(list)
	var last Step
	for _, st := range q.Steps {
		switch {
		case oneOf(st.Do, replies):
		case oneOf(st.Do, filing):
			last = st
		case st.Do == "category": // given, never taken off (Act toggles)
			if _, err := s.Category(ctx, userID, st.Arg); err != nil {
				return err
			}
			if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET categories = categories || $2::uuid WHERE id::text = ANY($1)
				AND NOT categories @> ARRAY[$2::uuid]`, all, st.Arg); err != nil {
				return err
			}
		case st.Do == "forward":
			for _, id := range all {
				if err := s.sendOn(ctx, userID, id, "forward", st.Arg, ""); err != nil {
					return err
				}
			}
		default:
			if err := s.Act(ctx, userID, all, st.Do, ""); err != nil {
				return err
			}
		}
	}
	switch last.Do {
	case "":
		return nil
	case "ignore":
		return s.ignore(ctx, userID, all, true)
	case "move":
		var move []string // the letters already there stay
		for _, l := range list {
			if l.folder != last.Arg {
				move = append(move, l.id)
			}
		}
		if len(move) == 0 {
			return nil
		}
		return s.Move(ctx, userID, move, last.Arg)
	}
	return s.Act(ctx, userID, all, last.Do, "")
}
