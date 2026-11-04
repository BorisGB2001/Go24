package matrixsync

import (
	"context"
	"time"

	"go24/internal/i18n"
)

// The tasks and meetings made of a chat's messages, as the «Задачи» and «Встречи» of the chat panel list them
// (go24 keeps the ties, im_chat_items; whether the person may see each task or meeting is for its own section).

// ChatItem is a task or a meeting tied to a chat.
type ChatItem struct {
	ItemID  string    // tasks.id or events.id
	EventID string    // the message it was made of ("" — made from the panel)
	At      time.Time // when it was tied
}

// TieItem ties the task (kind "task") or the meeting ("event") to the chat, as made of its message event ("" — of
// none). The employee must be in the chat.
func (s *Service) TieItem(ctx context.Context, userID, room, kind, itemID, event string) error {
	if err := s.inChat(ctx, userID, room); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO im_chat_items (room_id, kind, item_id, event_id, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`, room, kind, itemID, event, userID)
	return err
}

// ChatItems are the chat's tasks (kind "task") or meetings ("event"), the latest first; the employee must be in the
// chat.
func (s *Service) ChatItems(ctx context.Context, userID, room, kind string) ([]ChatItem, error) {
	if err := s.inChat(ctx, userID, room); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT item_id::text, event_id, created_at FROM im_chat_items
		WHERE room_id = $1 AND kind = $2 ORDER BY created_at DESC LIMIT 200`, room, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatItem
	for rows.Next() {
		var c ChatItem
		if err := rows.Scan(&c.ItemID, &c.EventID, &c.At); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// inChat checks the employee is in the chat now (one who left still reads its state as it was then).
func (s *Service) inChat(ctx context.Context, userID, room string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	var m struct {
		Membership string `json:"membership"`
	}
	if err := s.M.StateAs(ctx, tok, room, "m.room.member", me, &m); err != nil {
		return forbidden(err)
	}
	if m.Membership != "join" {
		return i18n.E("err.forbidden")
	}
	return nil
}

// ChatInfo is the chat's name and project (a collab's too; "" — none) for one in it, as a task or a meeting made from
// the chat's panel takes them.
func (s *Service) ChatInfo(ctx context.Context, userID, room string) (string, string, error) {
	if err := s.inChat(ctx, userID, room); err != nil {
		return "", "", err
	}
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return "", "", err
	}
	var name struct {
		Name string `json:"name"`
	}
	_ = s.M.StateAs(ctx, tok, room, "m.room.name", "", &name)
	return name.Name, s.roomProject(ctx, room), nil
}
