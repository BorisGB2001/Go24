package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"go24/internal/matrixsync"
)

// REST compatible with the portal, chat bots 2.0, the bot's chats (GO24DEV-954): imbot.v2.Chat.add, get, update,
// User.list; imbot.v2.Chat.Message.read, getContext, Reaction.add, delete; imbot.v2.Chat.TextField.enabled
// (GO24DEV-964). The bot acts as itself: the helpers of
// im.chat.* and im.dialog.* with the bot as the caller.

// portalV2As is the bot of the call (botId) and the call made as the bot.
func (s *Server) portalV2As(c portalCall) (matrixsync.Bot, portalCall, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return b, c, err
	}
	c.p.UserID = b.ID
	return b, c, s.portalNeedMatrix(c)
}

// portalV2Room is the room of dialogId the bot is in.
func (s *Server) portalV2Room(c portalCall) (string, error) {
	room, err := s.portalDialog(c, portalParam(c.in, "DIALOG_ID"))
	if err != nil {
		return "", err
	}
	if _, err := s.Matrix.ChatMembers(c.r.Context(), c.p.UserID, room); err != nil {
		return "", err
	}
	return room, nil
}

// portalV2Chat is the answer about a chat of the bot: {chat, users: [the bot]}.
func (s *Server) portalV2Chat(c portalCall, b matrixsync.Bot, room string) (portalOut, error) {
	chat, err := s.Matrix.V2Chat(c.r.Context(), b, room)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"chat": chat, "users": []any{matrixsync.BotV2User(b)}}}, nil
}

// imbot.v2.Chat.add: botId, fields (type chat or open, title, description, color, avatar, userIds, ownerId, message,
// entityType, entityId) → {chat, users}: a group chat the bot makes, with the bot in it.
func (s *Server) portalV2ChatAdd(c portalCall) (portalOut, error) {
	b, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	f := portalV2Fields(c)
	in := map[string]any{"TYPE": "CHAT"}
	if strings.EqualFold(portalV2Str(f, "type"), "open") {
		in["TYPE"] = "OPEN"
	}
	for _, k := range [][2]string{{"title", "TITLE"}, {"description", "DESCRIPTION"}, {"color", "COLOR"}, {"avatar", "AVATAR"},
		{"userIds", "USERS"}, {"ownerId", "OWNER_ID"}, {"message", "MESSAGE"}, {"entityType", "ENTITY_TYPE"}, {"entityId", "ENTITY_ID"}} {
		if v, ok := portalParamOK(f, k[0]); ok {
			in[k[1]] = v
		}
	}
	c.in = in
	out, err := s.portalChatAdd(c)
	if err != nil {
		return portalOut{}, err
	}
	room, err := s.portalChatRoom(c.r.Context(), portalStr(out.Result))
	if err != nil {
		return portalOut{}, err
	}
	return s.portalV2Chat(c, b, room)
}

// imbot.v2.Chat.get: botId, dialogId — a chat the bot is in → {chat, users}.
func (s *Server) portalV2ChatGet(c portalCall) (portalOut, error) {
	b, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	room, err := s.portalV2Room(c)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalV2Chat(c, b, room)
}

// imbot.v2.Chat.update: botId, dialogId («chatN»), fields (title, description, color, avatar) → {result: true}, as the
// chat's rights let the bot.
func (s *Server) portalV2ChatUpdate(c portalCall) (portalOut, error) {
	_, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	if !strings.HasPrefix(strings.ToLower(portalV2Str(c.in, "DIALOG_ID")), "chat") {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "WRONG_MESSAGE_TYPE", "err.portal_chat")
	}
	room, err := s.portalV2Room(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx, f := c.r.Context(), portalV2Fields(c)
	if _, ok := portalParamOK(f, "title"); ok {
		t := portalV2Str(f, "title")
		if t == "" {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "TITLE_EMPTY", "err.portal_title")
		}
		if err := s.Matrix.RenameGroup(ctx, c.p.UserID, room, t); err != nil {
			return portalOut{}, err
		}
	}
	if _, ok := portalParamOK(f, "description"); ok {
		if err := s.Matrix.SetGroupTopic(ctx, c.p.UserID, room, portalV2Str(f, "description")); err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(f, "color"); ok {
		c.in["COLOR"] = v
		if err := s.portalSetColor(c, room); err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(f, "avatar"); ok {
		c.in["AVATAR"] = v
		if err := s.portalAvatar(c, room); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: map[string]any{"result": true}}, nil
}

// imbot.v2.Chat.User.list: botId, dialogId, order {userId: ASC|DESC}, limit (1–200, 50) → the people of the chat (User).
func (s *Server) portalV2ChatUsers(c portalCall) (portalOut, error) {
	_, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	room, err := s.portalV2Room(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	mem, err := s.Matrix.ChatMembers(ctx, c.p.UserID, room)
	if err != nil {
		return portalOut{}, err
	}
	limit, _ := strconv.Atoi(portalV2Str(c.in, "limit"))
	if limit <= 0 {
		limit = 50
	}
	out := []map[string]any{}
	for _, m := range mem {
		if m.ID == "" {
			continue
		}
		u, err := s.Matrix.V2Person(ctx, m.ID)
		if err != nil {
			return portalOut{}, err
		}
		out = append(out, u)
	}
	if dir := strings.ToUpper(portalV2Str(portalMap(portalParam(c.in, "order")), "userId")); dir != "" {
		slices.SortStableFunc(out, func(a, b map[string]any) int {
			x, y := portalStr(a["id"]), portalStr(b["id"])
			d := len(x) - len(y) // numbers as numbers
			if d == 0 {
				d = strings.Compare(x, y)
			}
			if dir == "DESC" {
				return -d
			}
			return d
		})
	}
	return portalOut{Result: out[:min(len(out), limit, 200)]}, nil
}

// imbot.v2.Chat.Message.read: botId, dialogId, messageId (or all) → {chatId, lastId, counter, viewedMessages}.
func (s *Server) portalV2Read(c portalCall) (portalOut, error) {
	_, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalDialogRead(c)
	if err != nil {
		return portalOut{}, err
	}
	r := out.Result.(map[string]any)
	viewed := []int64{}
	if n, _ := r["lastId"].(int64); n > 0 {
		viewed = append(viewed, n)
	}
	return portalOut{Result: map[string]any{"chatId": r["chatId"], "lastId": r["lastId"], "counter": 0, "viewedMessages": viewed}}, nil
}

// imbot.v2.Chat.Message.getContext: botId, messageId, range (1–50, 50) → {messages — that many before and after it,
// oldest first —, users, hasPrevPage, hasNextPage}.
func (s *Server) portalV2Context(c portalCall) (portalOut, error) {
	b, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	room, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	n, _ := strconv.Atoi(portalV2Str(c.in, "range"))
	if n <= 0 || n > 50 {
		n = 50
	}
	ctx := c.r.Context()
	evs, authors, prev, next, err := s.Matrix.BotAround(ctx, b.ID, room, event, n)
	if err != nil {
		return portalOut{}, err
	}
	msgs, users, seen := []any{}, []any{}, map[string]bool{}
	for _, ev := range evs {
		id := authors[ev.Sender]
		m, err := s.Matrix.V2Message(ctx, room, ev, id)
		if err != nil {
			return portalOut{}, err
		}
		msgs = append(msgs, m)
		if id != "" && !seen[id] {
			seen[id] = true
			u, err := s.Matrix.V2Person(ctx, id)
			if err != nil {
				return portalOut{}, err
			}
			users = append(users, u)
		}
	}
	return portalOut{Result: map[string]any{"messages": msgs, "users": users, "hasPrevPage": prev, "hasNextPage": next}}, nil
}

// portalV2React puts (on) or takes back the bot's reaction (a code of imbot.v2) on the message messageId → {result: true}.
func (s *Server) portalV2React(c portalCall, on bool) (portalOut, error) {
	_, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	emoji, ok := matrixsync.ReactionEmoji(portalV2Str(c.in, "reaction"))
	if !ok {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "REACTION_NOT_FOUND", "err.imbot_reaction")
	}
	room, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Matrix.SetReaction(c.r.Context(), c.p.UserID, room, event, emoji, on); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"result": true}}, nil
}

// imbot.v2.Chat.Message.Reaction.add: botId, messageId, reaction (like, fire…) → {result: true}.
func (s *Server) portalV2ReactionAdd(c portalCall) (portalOut, error) {
	return s.portalV2React(c, true)
}

// imbot.v2.Chat.Message.Reaction.delete: botId, messageId, reaction → {result: true}.
func (s *Server) portalV2ReactionDelete(c portalCall) (portalOut, error) {
	return s.portalV2React(c, false)
}

// imbot.v2.Chat.TextField.enabled: botId, dialogId — a chat the bot is in —, enabled (true by default) → {result:
// true}; switched off, the chat's page shows a note in place of the input field (the bot's buttons still answer).
func (s *Server) portalV2TextField(c portalCall) (portalOut, error) {
	_, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	room, err := s.portalV2Room(c)
	if err != nil {
		return portalOut{}, err
	}
	on := true
	if y := portalFlagOf(c.in, "enabled"); y != nil {
		on = *y
	}
	ctx := c.r.Context()
	if _, err := s.portalChatNum(ctx, room); err != nil {
		return portalOut{}, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE portal_chats SET text_field = $2 WHERE room_id = $1`, room, on); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"result": true}}, nil
}
