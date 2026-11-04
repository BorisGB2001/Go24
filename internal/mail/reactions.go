package mail

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Reactions, as Outlook's: a letter between colleagues (its sender an employee of go24, and someone else of its
// people one too) takes a reaction of each employee who has it — 👍 ❤️ 🎉 😆 😮 😢. A reaction does not go by mail:
// go24 keeps it by the letter's Message-ID, so all who have the letter see it; its sender hears of it in the bell.

// ReactionKinds are the reactions offered, in Outlook's order.
var ReactionKinds = []string{"like", "heart", "party", "laugh", "wow", "sad"}

// ReactionEmoji is how each reaction looks.
var ReactionEmoji = map[string]string{"like": "👍", "heart": "❤️", "party": "🎉", "laugh": "😆", "wow": "😮", "sad": "😢"}

// Reaction is one reaction of a letter with all who chose it.
type Reaction struct {
	Kind  string   `json:"kind"`
	Emoji string   `json:"emoji"`
	Count int      `json:"count"`
	Who   []string `json:"who"`
}

// Reactions are those of a letter: whether it takes them (a letter between colleagues), the user's own ("" — none),
// each reaction chosen (in the order of ReactionKinds).
type Reactions struct {
	Open bool       `json:"open"`
	Mine string     `json:"mine"`
	List []Reaction `json:"list"`
}

// addrsOf are the addresses of a list («Имя <a@b>, c@d»), lower case.
func addrsOf(list string) []string {
	var out []string
	for _, a := range strings.Split(strings.ReplaceAll(list, ";", ","), ",") {
		a = strings.TrimSpace(a)
		if i := strings.LastIndex(a, "<"); i >= 0 {
			a = strings.TrimSuffix(a[i+1:], ">")
		}
		if a = strings.ToLower(strings.TrimSpace(a)); strings.Contains(a, "@") {
			out = append(out, a)
		}
	}
	return out
}

// colleagues are those of the addresses that belong to go24's employees (their own e-mail or a mailbox; no guests),
// each with the employee.
func (s *Store) colleagues(ctx context.Context, addrs []string) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT lower(u.email), u.id::text FROM users u WHERE lower(u.email) = ANY($1) AND NOT u.guest
		UNION SELECT lower(b.email), b.user_id::text FROM mailboxes b JOIN users u ON u.id = b.user_id WHERE lower(b.email) = ANY($1) AND NOT u.guest`, addrs)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for rows.Next() {
		var a, id string
		if err := rows.Scan(&a, &id); err != nil {
			rows.Close()
			return nil, err
		}
		out[a] = id
	}
	rows.Close()
	return out, rows.Err()
}

// reactable: does the letter take reactions — sent by a colleague (the user too), and someone of its people other
// than the user a colleague as well? The sender's employee is returned too ("" — none).
func (s *Store) reactable(ctx context.Context, userID string, m Message) (bool, string, error) {
	if m.MessageID == "" || m.Draft || m.FolderRole == "drafts" || m.FromAddr == "" {
		return false, "", nil
	}
	from := strings.ToLower(strings.TrimSpace(m.FromAddr))
	people := append([]string{from}, addrsOf(m.To+","+m.CC)...)
	known, err := s.colleagues(ctx, people)
	if err != nil {
		return false, "", err
	}
	author, ok := known[from]
	if !ok {
		return false, "", nil
	}
	for _, a := range people {
		if id, ok := known[a]; ok && id != userID {
			return true, author, nil
		}
	}
	return false, author, nil
}

// ReactionsOf are the reactions of the user's letters (by their ids in go24).
func (s *Store) ReactionsOf(ctx context.Context, userID string, letters []Message) (map[string]Reactions, error) {
	out := map[string]Reactions{}
	var mids []string
	for _, m := range letters {
		open, _, err := s.reactable(ctx, userID, m)
		if err != nil {
			return nil, err
		}
		if open {
			out[m.ID] = Reactions{Open: true}
			mids = append(mids, m.MessageID)
		}
	}
	if len(mids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT r.message_id, r.kind, r.user_id::text, coalesce(nullif(u.name, ''), u.email)
		FROM mail_reactions r JOIN users u ON u.id = r.user_id WHERE r.message_id = ANY($1) ORDER BY r.created_at`, mids)
	if err != nil {
		return nil, err
	}
	type row struct{ mid, kind, user, name string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.mid, &x.kind, &x.user, &x.name)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	for _, m := range letters {
		rs, ok := out[m.ID]
		if !ok {
			continue
		}
		for _, k := range ReactionKinds {
			re := Reaction{Kind: k, Emoji: ReactionEmoji[k]}
			for _, x := range list {
				if x.mid != m.MessageID || x.kind != k {
					continue
				}
				re.Count++
				re.Who = append(re.Who, x.name)
				if x.user == userID {
					rs.Mine = k
				}
			}
			if re.Count > 0 {
				rs.List = append(rs.List, re)
			}
		}
		out[m.ID] = rs
	}
	return out, nil
}

// LetterReactions are the reactions of the user's letter.
func (s *Store) LetterReactions(ctx context.Context, userID, id string) (Reactions, error) {
	m, err := s.message(ctx, userID, id)
	if err != nil {
		return Reactions{}, err
	}
	rs, err := s.ReactionsOf(ctx, userID, []Message{m})
	return rs[m.ID], err
}

// React sets the user's reaction to the letter: kind "" — takes it away. The letter's sender (when not the user)
// hears of a new reaction in the bell, with the link to the letter in their mail.
func (s *Store) React(ctx context.Context, userID, id, kind string) error {
	if kind != "" && !slices.Contains(ReactionKinds, kind) {
		return i18n.E("err.bad_form")
	}
	m, err := s.message(ctx, userID, id)
	if err != nil {
		return err
	}
	open, author, err := s.reactable(ctx, userID, m)
	if err != nil {
		return err
	}
	if !open {
		return i18n.E("mail.react.closed")
	}
	if kind == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM mail_reactions WHERE message_id = $1 AND user_id::text = $2`, m.MessageID, userID)
		return err
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO mail_reactions (message_id, user_id, kind) VALUES ($1, $2, $3)
		ON CONFLICT (message_id, user_id) DO UPDATE SET kind = EXCLUDED.kind, created_at = now() WHERE mail_reactions.kind <> EXCLUDED.kind`,
		m.MessageID, userID, kind)
	if err != nil || tag.RowsAffected() == 0 || author == "" || author == userID {
		return err
	}
	url := "/mail" // the sender's own copy of the letter, «Отправленные» first
	var folder, letter string
	err = s.db.QueryRow(ctx, `SELECT x.folder_id::text, x.id::text FROM mail_messages x JOIN mailboxes b ON b.id = x.mailbox_id
		JOIN mail_folders f ON f.id = x.folder_id WHERE b.user_id::text = $1 AND x.message_id = $2
		ORDER BY f.role = 'sent' DESC, x.sent_at LIMIT 1`, author, m.MessageID).Scan(&folder, &letter)
	if err == nil {
		url = "/mail?folder=" + folder + "&letter=" + letter
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var who string
	if err := s.db.QueryRow(ctx, `SELECT coalesce(nullif(name, ''), email) FROM users WHERE id::text = $1`, userID).Scan(&who); err != nil {
		return err
	}
	subject := m.Subject
	if subject == "" {
		subject = s.tr("mail.no_subject")
	}
	_, err = s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'mail', left($2, 300), $3)`,
		author, s.tr("mail.react.notify", who, ReactionEmoji[kind], subject), url)
	return err
}
