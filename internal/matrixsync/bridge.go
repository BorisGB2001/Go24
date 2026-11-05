package matrixsync

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/matrix"
)

// A group chat of go24 linked to a group of an outside messenger (notifyout: Telegram): the bridge — a bot built into
// go24 — is a member of the chat; it reads the chat's new messages for the group and writes there the group's
// messages of people who are not employees (with their name). An employee whose own chat with the company's bot is
// linked writes as themselves. A message that came from the group carries bridgedKey, so it does not go back.

// Bridge is the builtin name of the bridge bot.
const Bridge = "bridge"

// bridgedKey marks a message posted from the outside group.
const bridgedKey = "go24.bridged"

// bridge makes the bridge bot with its Matrix account on first use, keeping its name (BridgeName).
func (s *Service) bridge(ctx context.Context) (Bot, error) {
	name := strings.TrimSpace(s.BridgeName)
	if name == "" {
		name = "Telegram"
	}
	b, err := scanBot(s.DB.QueryRow(ctx, `SELECT `+botCols+` FROM im_bots b WHERE b.builtin = $1`, Bridge))
	if errors.Is(err, pgx.ErrNoRows) {
		b.mxid = s.M.UserID(Bridge)
		err = s.DB.QueryRow(ctx, `INSERT INTO im_bots (builtin, code, name, mxid, hidden) VALUES ($1, $1, $2, $3, true)
			ON CONFLICT (builtin) WHERE builtin <> '' DO UPDATE SET name = EXCLUDED.name RETURNING id::text`, Bridge, name, b.mxid).Scan(&b.ID)
		b.Name = ""
	}
	if err != nil || b.Name == name {
		return b, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE im_bots SET name = $2 WHERE id::text = $1`, b.ID, name); err != nil {
		return b, err
	}
	b.Name = name
	return b, s.M.PutUser(ctx, b.mxid, name, "", "", false)
}

// CanBridge: the employee may link the group chat to an outside group — they may rename it.
func (s *Service) CanBridge(ctx context.Context, userID, room string) bool {
	g, err := s.GroupInfo(ctx, userID, room)
	return err == nil && g != nil && g.CanEdit
}

// BridgeJoin brings the bridge bot into the group chat, invited by the employee who links it.
func (s *Service) BridgeJoin(ctx context.Context, userID, room string) error {
	b, err := s.bridge(ctx)
	if err != nil {
		return err
	}
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	mem, err := s.M.Members(ctx, room, tok)
	if err != nil {
		return forbidden(err)
	}
	if _, in := mem[b.mxid]; in {
		return nil
	}
	if err := s.M.InviteAs(ctx, tok, room, b.mxid); err != nil {
		return forbidden(err)
	}
	_, btok, err := s.token(ctx, b.ID)
	if err != nil {
		return err
	}
	return s.M.JoinAs(ctx, btok, room)
}

// BridgeLeave takes the bridge bot out of the chat (the link is gone).
func (s *Service) BridgeLeave(ctx context.Context, room string) error {
	b, err := s.bridge(ctx)
	if err != nil {
		return err
	}
	return s.BotLeave(ctx, b.ID, room)
}

// BridgeNews: the new messages of the chats the bridge is in since the position (from "" — none, only the current
// position), except its own and those that came from the outside group; the next position.
func (s *Service) BridgeNews(ctx context.Context, since string) ([]ChatNew, string, error) {
	b, err := s.bridge(ctx)
	if err != nil {
		return nil, "", err
	}
	me, tok, err := s.token(ctx, b.ID)
	if err != nil {
		return nil, "", err
	}
	if since == "" {
		sy, err := s.M.SyncAs(ctx, tok, "", 0, 0)
		if err != nil {
			return nil, "", err
		}
		return nil, sy.NextBatch, nil
	}
	sy, err := s.M.SyncAs(ctx, tok, since, 0, 50)
	if err != nil {
		return nil, "", err
	}
	var out []ChatNew
	for id, r := range sy.Rooms.Join {
		var evs []matrix.Event // convert wants newest first
		for i := len(r.Timeline.Events) - 1; i >= 0; i-- {
			if ev := r.Timeline.Events[i]; ev.Content[bridgedKey] == nil {
				evs = append(evs, ev)
			}
		}
		for _, m := range s.convert(ctx, evs, id, me, false) {
			if m.Mine || m.Notice || m.Event != "" || m.Call != nil || m.Change != "" {
				continue
			}
			text := m.Body
			if m.File != nil {
				text = "📎 " + m.File.Name
			}
			out = append(out, ChatNew{Room: id, Event: m.ID, Root: m.ThreadRoot, Sender: m.SenderName, Text: text})
		}
	}
	return out, sy.NextBatch, nil
}

// BridgePost writes a message of the outside group into the chat (into the thread of root, when given): as the
// employee (userID) when they are in the chat, as the bridge bot with the name of who wrote otherwise.
func (s *Service) BridgePost(ctx context.Context, userID, room, root, name, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	b, err := s.bridge(ctx)
	if err != nil {
		return err
	}
	if userID != "" {
		if me, tok, err := s.token(ctx, userID); err == nil {
			if mem, err := s.M.Members(ctx, room, tok); err == nil && mem[me] == "join" {
				content := textContent(text)
				content[bridgedKey] = true
				_, err = s.post(ctx, me, tok, room, root, content)
				return err
			}
		}
	}
	me, tok, err := s.token(ctx, b.ID)
	if err != nil {
		return err
	}
	content := textContent(name + ": " + text)
	content[bridgedKey] = true
	_, err = s.post(ctx, me, tok, room, root, content)
	return err
}
