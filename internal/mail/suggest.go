package mail

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// «Предлагаемые ответы», as Outlook's suggested replies: under a letter received the AI (the web package has the
// user's connection make them) offers a few short answers; a click opens the reply with one. They are kept with the
// letter, so it opens again without a new call.

// Suggestion limits: how many answers, how long each.
const (
	MaxSuggestions = 3
	maxSuggestion  = 80
)

// Suggestable tells whether a letter is one answers are offered to: received, not a draft, not a mailing, not in
// the junk or the trash.
func Suggestable(m Message) bool {
	return !m.Outgoing() && !m.Draft && !m.Unsubscribe && m.FolderRole != "junk" && m.FolderRole != "trash"
}

// Suggested are the kept answers of the user's letter (false — none asked for yet; true with none — it needs no answer).
func (s *Store) Suggested(ctx context.Context, userID, id string) ([]string, bool, error) {
	var out []string
	err := s.db.QueryRow(ctx, `SELECT g.replies FROM mail_suggestions g
		JOIN mail_messages x ON x.id = g.message_id JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2`, id, userID).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return out, err == nil, err
}

// SaveSuggestions keeps the answers offered to the user's letter: trimmed, on one line, no repeats, at most
// MaxSuggestions of maxSuggestion letters.
func (s *Store) SaveSuggestions(ctx context.Context, userID, id string, replies []string) ([]string, error) {
	out := []string{}
	for _, r := range replies {
		r = strings.Join(strings.Fields(r), " ")
		if utf8.RuneCountInString(r) > maxSuggestion {
			r = string([]rune(r)[:maxSuggestion-1]) + "…"
		}
		if r == "" || len(out) == MaxSuggestions || containsFold(out, r) {
			continue
		}
		out = append(out, r)
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO mail_suggestions (message_id, replies)
		SELECT x.id, $3 FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2
		ON CONFLICT (message_id) DO UPDATE SET replies = EXCLUDED.replies, created_at = now()`, id, userID, out)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return out, err
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
