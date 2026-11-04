package matrixsync

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The bot events posts them (GO24DEV-925): a handler bound by the REST (/rest/imbot.register)
// knows chats as «chatN», people, bots and commands by numbers and messages by numbers too; botQueue puts that copy of
// the event into Portal_DATA, apps hands it to such a handler instead of go24's own.

// PortalChatNum is the number of a Matrix room, given when an integration first meets it (migration 665).
func (s *Service) PortalChatNum(ctx context.Context, room string) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `WITH ins AS (INSERT INTO portal_chats (room_id) VALUES ($1) ON CONFLICT DO NOTHING RETURNING num)
		SELECT num FROM ins UNION ALL SELECT num FROM portal_chats WHERE room_id = $1 LIMIT 1`, room).Scan(&n)
	return n, err
}

// PortalMessageNum is the number of a message.
func (s *Service) PortalMessageNum(ctx context.Context, room, event string) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `WITH ins AS (INSERT INTO portal_messages (room_id, event_id) VALUES ($1, $2) ON CONFLICT DO NOTHING RETURNING num)
		SELECT num FROM ins UNION ALL SELECT num FROM portal_messages WHERE event_id = $2 LIMIT 1`, room, event).Scan(&n)
	return n, err
}

// portalPerson is the number of an employee or a bot by its go24 ID; the ID itself when it is neither.
func (s *Service) portalPerson(ctx context.Context, id string) (any, error) {
	if id == "" {
		return id, nil
	}
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT num FROM users WHERE id::text = $1 UNION ALL SELECT num FROM im_bots WHERE id::text = $1 LIMIT 1`,
		id).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, nil
	}
	return n, err
}

// portalEvent is the copy of a bot event's data for a handler.
func (s *Service) portalEvent(ctx context.Context, b Bot, data map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range data {
		out[k] = v
	}
	if _, ok := data["BOT_ID"]; ok {
		out["BOT_ID"] = b.Num
	}
	out["BOT"] = map[string]any{strconv.FormatInt(b.Num, 10): map[string]any{"BOT_ID": b.Num, "BOT_CODE": b.Code}}
	room := ""
	if p, ok := data["PARAMS"].(map[string]any); ok {
		q := map[string]any{}
		for k, v := range p {
			q[k] = v
		}
		room, _ = p["CHAT_ID"].(string)
		if room != "" {
			n, err := s.PortalChatNum(ctx, room)
			if err != nil {
				return nil, err
			}
			q["CHAT_ID"] = n
			if _, ok := p["TO_CHAT_ID"]; ok {
				q["TO_CHAT_ID"] = n
			}
			if d, _ := p["DIALOG_ID"].(string); strings.HasPrefix(d, "!") {
				q["DIALOG_ID"] = "chat" + strconv.FormatInt(n, 10)
			}
		}
		for _, k := range []string{"MESSAGE_ID", "THREAD_ID"} {
			if ev, _ := p[k].(string); ev != "" && room != "" {
				n, err := s.PortalMessageNum(ctx, room, ev)
				if err != nil {
					return nil, err
				}
				q[k] = n
			}
		}
		for _, k := range []string{"DIALOG_ID", "FROM_USER_ID", "TO_USER_ID", "USER_ID"} {
			if id, _ := q[k].(string); id != "" && !strings.HasPrefix(id, "chat") {
				n, err := s.portalPerson(ctx, id)
				if err != nil {
					return nil, err
				}
				q[k] = n
			}
		}
		out["PARAMS"] = q
	}
	if u, ok := data["USER"].(map[string]any); ok {
		q := map[string]any{}
		for k, v := range u {
			q[k] = v
		}
		id, _ := u["ID"].(string)
		n, err := s.portalPerson(ctx, id)
		if err != nil {
			return nil, err
		}
		q["ID"] = n
		out["USER"] = q
	}
	if cmds, ok := data["COMMAND"].(map[string]any); ok {
		q := map[string]any{}
		for id, v := range cmds {
			c, _ := v.(map[string]any)
			var num int64
			if err := s.DB.QueryRow(ctx, `SELECT num FROM im_bot_commands WHERE id::text = $1`, id).Scan(&num); err != nil {
				return nil, err
			}
			cc := map[string]any{}
			for k, v := range c {
				cc[k] = v
			}
			cc["BOT_ID"], cc["COMMAND_ID"] = b.Num, num
			if ev, _ := c["MESSAGE_ID"].(string); ev != "" && room != "" {
				n, err := s.PortalMessageNum(ctx, room, ev)
				if err != nil {
					return nil, err
				}
				cc["MESSAGE_ID"] = n
			}
			q[strconv.FormatInt(num, 10)] = cc
		}
		out["COMMAND"] = q
	}
	return out, nil
}

// botToken is the bot's Matrix account; ErrNoAccount for a bot that is gone.
func (s *Service) botToken(ctx context.Context, botID string) (string, string, error) {
	mx, tok, err := s.botPuppet(ctx, botID)
	if err == nil && tok == "" {
		err = ErrNoAccount
	}
	return mx, tok, err
}

// BotTyping: the others see «typing…» of the bot in the room (imbot.chat.sendTyping).
func (s *Service) BotTyping(ctx context.Context, botID, room string) error {
	mx, tok, err := s.botToken(ctx, botID)
	if err != nil {
		return err
	}
	return forbidden(s.M.Typing(ctx, tok, room, mx, true))
}

// BotLeave: the bot leaves a chat (imbot.chat.leave).
func (s *Service) BotLeave(ctx context.Context, botID, room string) error {
	_, tok, err := s.botToken(ctx, botID)
	if err != nil {
		return err
	}
	return forbidden(s.M.LeaveAs(ctx, tok, room))
}

// CommandOf is the command of a bot of the application by its number (COMMAND_ID).
func (s *Service) CommandOf(ctx context.Context, appID, num string) (Command, error) {
	var bot string
	err := s.DB.QueryRow(ctx, `SELECT c.bot_id::text FROM im_bot_commands c JOIN im_bots b ON b.id = c.bot_id
		WHERE c.num::text = $2 AND b.app_id::text = $1`, appID, strings.TrimSpace(num)).Scan(&bot)
	if errors.Is(err, pgx.ErrNoRows) {
		return Command{}, i18n.E("err.not_found")
	}
	if err != nil {
		return Command{}, err
	}
	return s.BotCommand(ctx, appID, bot, num)
}
