package mail

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// Searching the mail as in Outlook: words, or what Outlook's search box takes — from: to: subject: has:attachment
// is:unread is:read is:flagged is:important before: after: (and the Russian from:/to: of Russian Outlook: от: кому:
// тема:), quotes keep words together. The recent searches are suggested again; a saved one is a search folder.

// Query is a search, parsed.
type Query struct {
	Words, From, To, Subject            []string
	Attach, Unread, Read, Flagged, High bool
	After, Before                       *time.Time // from the day on; till the day (not including it)
}

// ops are the search's operators by their names (the Russian ones too).
var ops = map[string]string{"from": "from", "от": "from", "to": "to", "кому": "to", "cc": "to", "subject": "subject", "тема": "subject",
	"has": "has", "есть": "has", "is": "is", "before": "before", "до": "before", "after": "after", "после": "after"}

// tokens split a search by spaces, quotes keeping words together (op:"two words" too).
func tokens(q string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	for _, r := range q {
		switch {
		case r == '"':
			quoted = !quoted
		case !quoted && (r == ' ' || r == '\t' || r == '\n'):
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// searchDate: YYYY-MM-DD or DD.MM.YYYY.
func searchDate(v string) (*time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "02.01.2006", "2.1.2006"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return &t, true
		}
	}
	return nil, false
}

// ParseQuery reads a search; what is not an operator is a word.
func ParseQuery(q string) Query {
	var out Query
	for _, t := range tokens(q) {
		name, v, ok := strings.Cut(t, ":")
		op := ops[strings.ToLower(name)]
		if !ok || op == "" || v == "" {
			out.Words = append(out.Words, t)
			continue
		}
		v = strings.TrimSpace(v)
		switch lv := strings.ToLower(v); op {
		case "from":
			out.From = append(out.From, v)
		case "to":
			out.To = append(out.To, v)
		case "subject":
			out.Subject = append(out.Subject, v)
		case "has":
			if strings.HasPrefix(lv, "attach") || strings.HasPrefix(lv, "влож") || lv == "file" || lv == "files" {
				out.Attach = true
			} else {
				out.Words = append(out.Words, t)
			}
		case "is":
			switch lv {
			case "unread", "непрочитанные", "непрочитано":
				out.Unread = true
			case "read", "прочитанные", "прочитано":
				out.Read = true
			case "flagged", "flag", "сфлажком", "флажок":
				out.Flagged = true
			case "important", "high", "важные", "важно":
				out.High = true
			default:
				out.Words = append(out.Words, t)
			}
		case "before", "after":
			d, ok := searchDate(v)
			if !ok {
				out.Words = append(out.Words, t)
			} else if op == "before" {
				out.Before = d
			} else {
				out.After = d
			}
		}
	}
	return out
}

// Empty: nothing to search for.
func (q Query) Empty() bool {
	return len(q.Words)+len(q.From)+len(q.To)+len(q.Subject) == 0 && !q.Attach && !q.Unread && !q.Read && !q.Flagged && !q.High &&
		q.After == nil && q.Before == nil
}

// Terms are the words the list marks in the letters found.
func (q Query) Terms() []string {
	var out []string
	for _, l := range [][]string{q.Words, q.From, q.To, q.Subject} {
		out = append(out, l...)
	}
	return out
}

// Advanced is what Outlook's advanced search form gives.
type Advanced struct {
	From, To, Subject, Words, After, Before string
	Attach, Unread, Flagged                 bool
}

// String is the search the form makes (shown in the search box).
func (a Advanced) String() string {
	var parts []string
	add := func(op, v string) {
		if v = strings.TrimSpace(strings.ReplaceAll(v, `"`, "")); v == "" {
			return
		}
		if strings.ContainsAny(v, " \t") {
			v = `"` + v + `"`
		}
		parts = append(parts, op+v)
	}
	add("from:", a.From)
	add("to:", a.To)
	add("subject:", a.Subject)
	if a.Attach {
		parts = append(parts, "has:attachment")
	}
	if a.Unread {
		parts = append(parts, "is:unread")
	}
	if a.Flagged {
		parts = append(parts, "is:flagged")
	}
	add("after:", a.After)
	add("before:", a.Before)
	if w := strings.TrimSpace(a.Words); w != "" {
		parts = append(parts, w)
	}
	return strings.Join(parts, " ")
}

// Advanced is the search in the advanced search form's fields.
func (q Query) Advanced() Advanced {
	a := Advanced{From: strings.Join(q.From, " "), To: strings.Join(q.To, " "), Subject: strings.Join(q.Subject, " "),
		Words: strings.Join(q.Words, " "), Attach: q.Attach, Unread: q.Unread, Flagged: q.Flagged}
	if q.After != nil {
		a.After = q.After.Format("2006-01-02")
	}
	if q.Before != nil {
		a.Before = q.Before.Format("2006-01-02")
	}
	return a
}

// likeOf is an ILIKE pattern of the words.
func likeOf(v string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(v)) + "%"
}

// sqlArgs numbers the arguments of a query being made.
type sqlArgs []any

func (a *sqlArgs) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// where is the search's condition on the letters (x) — every part of it is to hold.
func (q Query) where(args *sqlArgs) string {
	var conds []string
	for _, w := range q.Words {
		p := args.add(likeOf(w))
		conds = append(conds, `(x.subject ILIKE `+p+` OR x.from_addr ILIKE `+p+` OR x.from_name ILIKE `+p+` OR x.body ILIKE `+p+
			` OR x.to_addr ILIKE `+p+` OR x.cc_addr ILIKE `+p+`)`)
	}
	for _, v := range q.From {
		p := args.add(likeOf(v))
		conds = append(conds, `(x.from_addr ILIKE `+p+` OR x.from_name ILIKE `+p+`)`)
	}
	for _, v := range q.To {
		p := args.add(likeOf(v))
		conds = append(conds, `(x.to_addr ILIKE `+p+` OR x.cc_addr ILIKE `+p+` OR x.bcc_addr ILIKE `+p+`)`)
	}
	for _, v := range q.Subject {
		conds = append(conds, `x.subject ILIKE `+args.add(likeOf(v)))
	}
	if q.Attach {
		conds = append(conds, `EXISTS (SELECT 1 FROM files fl WHERE fl.mail_id = x.id AND fl.content_id = '')`)
	}
	if q.Unread {
		conds = append(conds, `NOT x.seen`)
	}
	if q.Read {
		conds = append(conds, `x.seen`)
	}
	if q.Flagged {
		conds = append(conds, `x.flagged`)
	}
	if q.High {
		conds = append(conds, `x.importance = 1`)
	}
	if q.After != nil {
		conds = append(conds, `x.sent_at >= `+args.add(*q.After))
	}
	if q.Before != nil {
		conds = append(conds, `x.sent_at < `+args.add(*q.Before))
	}
	if len(conds) == 0 {
		return "true"
	}
	return strings.Join(conds, " AND ")
}

// Search is a saved search (a search folder) or a recent one (Name "").
type Search struct {
	ID, Name, Q string
}

// SearchFolder + a saved search's id is its folder.
const SearchFolder = "search:"

// maxRecent: so many recent searches are kept.
const maxRecent = 10

// Remember keeps a search as a recent one (the oldest go).
func (s *Store) Remember(ctx context.Context, userID, q string) error {
	q = strings.TrimSpace(q)
	if q == "" || utf8.RuneCountInString(q) > 500 {
		return nil
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO mail_searches (user_id, q) VALUES ($1, $2)
		ON CONFLICT (user_id, q) WHERE name = '' DO UPDATE SET used_at = now()`, userID, q); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM mail_searches WHERE user_id::text = $1 AND name = '' AND id NOT IN (
		SELECT id FROM mail_searches WHERE user_id::text = $1 AND name = '' ORDER BY used_at DESC LIMIT $2)`, userID, maxRecent)
	return err
}

// Recent are the user's recent searches with the words, the latest first.
func (s *Store) Recent(ctx context.Context, userID, prefix string, limit int) ([]Search, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, q FROM mail_searches WHERE user_id::text = $1 AND name = '' AND q ILIKE $2
		ORDER BY used_at DESC LIMIT $3`, userID, likeOf(prefix), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Search])
}

// ForgetRecent clears the user's recent searches.
func (s *Store) ForgetRecent(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM mail_searches WHERE user_id::text = $1 AND name = ''`, userID)
	return err
}

// Searches are the user's saved searches, by name.
func (s *Store) Searches(ctx context.Context, userID string) ([]Search, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, q FROM mail_searches WHERE user_id::text = $1 AND name <> '' ORDER BY lower(name)`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Search])
}

// SavedSearch is a saved search of the user.
func (s *Store) SavedSearch(ctx context.Context, userID, id string) (Search, error) {
	var x Search
	err := s.db.QueryRow(ctx, `SELECT id::text, name, q FROM mail_searches WHERE id::text = $1 AND user_id::text = $2 AND name <> ''`,
		id, userID).Scan(&x.ID, &x.Name, &x.Q)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// SaveSearch saves a search as a search folder (id "") or renames / changes one.
func (s *Store) SaveSearch(ctx context.Context, userID, id, name, q string) (string, error) {
	name, q = strings.TrimSpace(name), strings.TrimSpace(q)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", i18n.E("err.mail_search_name")
	}
	if q == "" || utf8.RuneCountInString(q) > 500 || ParseQuery(q).Empty() {
		return "", i18n.E("err.mail_search_q")
	}
	var err error
	if id == "" {
		err = s.db.QueryRow(ctx, `INSERT INTO mail_searches (user_id, name, q) VALUES ($1, $2, $3) RETURNING id::text`, userID, name, q).Scan(&id)
	} else {
		var tag pgconn.CommandTag
		tag, err = s.db.Exec(ctx, `UPDATE mail_searches SET name = $3, q = $4 WHERE id::text = $1 AND user_id::text = $2 AND name <> ''`, id, userID, name, q)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", i18n.E("err.mail_search_exists")
	}
	return id, err
}

// DeleteSearch deletes a saved search of the user.
func (s *Store) DeleteSearch(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_searches WHERE id::text = $1 AND user_id::text = $2 AND name <> ''`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}
