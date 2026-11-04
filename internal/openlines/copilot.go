package openlines

import (
	"context"
	"strings"
	"time"
)

// Conversation is the dialogue as text for a reader (a lead's comment, CoPilot): «who: text» a message; service lines
// and hidden messages are left out.
func Conversation(x Session, msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		who := x.Title()
		switch {
		case m.Internal():
			continue
		case m.Auto():
			who = x.Widget
		case m.Operator != "":
			who = m.Operator
		}
		b.WriteString(who + ": " + m.Text() + "\n")
	}
	return b.String()
}

// SetSummary keeps CoPilot's summary of the dialogue.
func (s *Store) SetSummary(ctx context.Context, id, text string) error {
	tag, err := s.db.Exec(ctx, `UPDATE ol_sessions SET summary = $2, summary_tried = true WHERE id::text = $1`, id, text)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// ToSummarize are the dialogues CoPilot sums up by itself: closed in the last day in a line with «CoPilot: итоги
// диалога», not spam, with the client's words and no summary yet. Each is given once (till it is opened again).
func (s *Store) ToSummarize(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.db.Query(ctx, `UPDATE ol_sessions s SET summary_tried = true FROM ol_widgets w
		WHERE w.id = coalesce(s.queue_id, s.widget_id) AND w.ai_summary AND s.status = 'closed' AND NOT s.spam AND NOT s.summary_tried
		AND s.closed_at > $1::timestamptz - interval '1 day'
		AND EXISTS (SELECT 1 FROM ol_messages m WHERE m.session_id = s.id AND m.kind = '' AND m.author_id IS NULL)
		RETURNING s.id::text`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
