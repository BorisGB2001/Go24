package tasks

import (
	"context"
	"strings"
)

// Reaction is one emoji under a task chat message: how many people put it, whether the viewer did, and who.
type Reaction struct {
	Key   string
	Count int
	Mine  bool
	Names string
}

// QuickReactions are offered under messages (any emoji from the messenger is shown too).
var QuickReactions = []string{"👍", "✅", "❤️", "😂", "🔥", "👀"}

// FillReactions loads the reactions of the messages for the viewer.
func (s *Store) FillReactions(ctx context.Context, viewer string, list []Comment) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	at := map[string]int{}
	for i, c := range list {
		ids[i] = c.ID
		at[c.ID] = i
	}
	rows, err := s.db.Query(ctx, `SELECT r.comment_id::text, r.key, count(*), bool_or(r.user_id = $2::uuid),
			string_agg(u.name, ', ' ORDER BY r.created_at)
		FROM task_comment_reactions r JOIN users u ON u.id = r.user_id
		WHERE r.comment_id = ANY($1::uuid[]) GROUP BY r.comment_id, r.key ORDER BY min(r.created_at)`, ids, viewer)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var r Reaction
		if err := rows.Scan(&id, &r.Key, &r.Count, &r.Mine, &r.Names); err != nil {
			return err
		}
		list[at[id]].Reactions = append(list[at[id]].Reactions, r)
	}
	return rows.Err()
}

// SetReaction puts (on) or takes back the person's reaction on a message; event is the reaction in the messenger.
// It reports the messenger event of a reaction taken back ("" when none).
func (s *Store) SetReaction(ctx context.Context, userID, taskID, commentID, key string, on bool, event string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 64 {
		return "", invalid("err.bad_form")
	}
	if err := s.CheckVisible(ctx, userID, taskID); err != nil {
		return "", err
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_comments WHERE id = $1 AND task_id = $2)`, commentID, taskID).Scan(&ok); err != nil || !ok {
		if err == nil {
			err = ErrNotFound
		}
		return "", err
	}
	if on {
		_, err := s.db.Exec(ctx, `INSERT INTO task_comment_reactions (comment_id, user_id, key, matrix_event) VALUES ($1, $2, $3, $4)
			ON CONFLICT (comment_id, user_id, key) DO UPDATE SET matrix_event = EXCLUDED.matrix_event`, commentID, userID, key, event)
		return "", err
	}
	var old string
	err := s.db.QueryRow(ctx, `DELETE FROM task_comment_reactions WHERE comment_id = $1 AND user_id = $2 AND key = $3
		RETURNING matrix_event`, commentID, userID, key).Scan(&old)
	if err != nil && strings.Contains(err.Error(), "no rows") {
		return "", nil
	}
	return old, err
}

// ReactionByEvent removes a reaction taken back in the messenger (its redaction); it reports whether there was one.
func (s *Store) ReactionByEvent(ctx context.Context, event string) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM task_comment_reactions WHERE matrix_event = $1`, event)
	return tag.RowsAffected() > 0, err
}

// HasReaction tells whether the person has put this reaction on the message.
func (s *Store) HasReaction(ctx context.Context, userID, commentID, key string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_comment_reactions WHERE comment_id = $1 AND user_id = $2 AND key = $3)`,
		commentID, userID, key).Scan(&ok)
	return ok, err
}
