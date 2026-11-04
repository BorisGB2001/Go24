package web

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/notify"
)

// REST compatible with the portal, the messenger's chats and reading (GO24DEV-909): im.chat.add, im.chat.user.add,
// im.chat.user.delete, im.chat.leave, im.chat.updateTitle, im.chat.updateAvatar, im.chat.setOwner; im.recent.get and
// im.recent.list; im.dialog.read; im.notify.read, im.notify.read.all, im.notify.confirm. The rights are the chat's
// own, as in the messenger.

func init() {
	for name, m := range map[string]portalMethod{
		"im.chat.add":          {Scope: "im:write", Run: (*Server).portalChatAdd},
		"im.chat.user.add":     {Scope: "im:write", Run: (*Server).portalChatUserAdd},
		"im.chat.user.delete":  {Scope: "im:write", Run: (*Server).portalChatUserDelete},
		"im.chat.leave":        {Scope: "im:write", Run: (*Server).portalChatLeave},
		"im.chat.updateTitle":  {Scope: "im:write", Run: (*Server).portalChatTitle},
		"im.chat.updateAvatar": {Scope: "im:write", Run: (*Server).portalChatAvatar},
		"im.chat.setOwner":     {Scope: "im:write", Run: (*Server).portalChatOwner},
		"im.recent.get":        {Scope: "im:read", Run: (*Server).portalRecentGet},
		"im.recent.list":       {Scope: "im:read", Run: (*Server).portalRecentList},
		"im.dialog.read":       {Scope: "im:read", Run: (*Server).portalDialogRead},
		"im.notify.read":       {Scope: "im:read", Run: (*Server).portalNotifyRead},
		"im.notify.read.all":   {Scope: "im:read", Run: (*Server).portalNotifyReadAll},
		"im.notify.confirm":    {Scope: "im:write", Run: (*Server).portalNotifyConfirm},
	} {
		portalMethods[strings.ToLower(name)] = m // looked up in lower case
	}
}

// portalChat is the room of CHAT_ID.
func (s *Server) portalChat(c portalCall) (string, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return "", err
	}
	chat := strings.TrimSpace(portalStr(portalParam(c.in, "CHAT_ID")))
	if chat == "" || chat == "0" {
		return "", s.portalErr(c.r, http.StatusBadRequest, "CHAT_ID_EMPTY", "err.portal_chat")
	}
	return s.portalDialog(c, "chat"+strings.TrimPrefix(strings.ToLower(chat), "chat"))
}

// portalChatPerson is the go24 id of an employee's number — or a chat bot's: its number is of the same row.
func (s *Server) portalChatPerson(c portalCall, v any) (string, error) {
	id, err := s.portalNum(c.r.Context(), "users", v)
	var ie *i18n.Error
	if errors.As(err, &ie) && ie.Key == "err.not_found" {
		if qe := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM im_bots WHERE num::text = $1`, strings.TrimSpace(portalStr(v))).Scan(&id); qe == nil {
			return id, nil
		}
	}
	return id, err
}

// portalChatPeople are the go24 ids of the employees (and bots) USERS (their numbers).
func (s *Server) portalChatPeople(c portalCall) ([]string, error) {
	var out []string
	for _, v := range portalList(portalParam(c.in, "USERS")) {
		id, err := s.portalChatPerson(c, v)
		if err != nil {
			return nil, err
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// portalAvatar sets the chat's picture from AVATAR, an image in base64 (a data: URL too).
func (s *Server) portalAvatar(c portalCall, room string) error {
	v := strings.TrimSpace(portalStr(portalParam(c.in, "AVATAR")))
	if _, data, ok := strings.Cut(v, ";base64,"); ok {
		v = data
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(raw) == 0 {
		return s.portalErr(c.r, http.StatusBadRequest, "AVATAR_ERROR", "err.portal_avatar")
	}
	mime := http.DetectContentType(raw)
	return s.Matrix.SetGroupPhoto(c.r.Context(), c.p.UserID, room, "avatar", mime, bytes.NewReader(raw), int64(len(raw)))
}

// im.chat.add: a group chat of the caller — TYPE CHAT (OPEN: an open one), CHANNEL or OPEN_CHANNEL; TITLE,
// DESCRIPTION, USERS, AVATAR, MESSAGE (the first message), OWNER_ID (hands it over), COLOR, ENTITY_TYPE and
// ENTITY_ID (an object of the application the chat is bound to). The chat's number.
func (s *Server) portalChatAdd(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	users, err := s.portalChatPeople(c)
	if err != nil {
		return portalOut{}, err
	}
	typ := strings.ToUpper(strings.TrimSpace(portalStr(portalParam(c.in, "TYPE"))))
	open, channel := strings.HasPrefix(typ, "OPEN"), strings.HasSuffix(typ, "CHANNEL")
	room, err := s.Matrix.CreateOpenGroup(ctx, c.p.UserID, portalStr(portalParam(c.in, "TITLE")), users, channel, open)
	if err != nil {
		return portalOut{}, err
	}
	if d := strings.TrimSpace(portalStr(portalParam(c.in, "DESCRIPTION"))); d != "" {
		if err := s.Matrix.SetGroupTopic(ctx, c.p.UserID, room, d); err != nil {
			return portalOut{}, err
		}
	}
	if portalStr(portalParam(c.in, "AVATAR")) != "" {
		if err := s.portalAvatar(c, room); err != nil {
			return portalOut{}, err
		}
	}
	if m := strings.TrimSpace(botText(portalStr(portalParam(c.in, "MESSAGE")))); m != "" {
		if _, err := s.Matrix.Send(ctx, c.p.UserID, room, m, ""); err != nil {
			return portalOut{}, err
		}
	}
	if owner, err := s.portalNum(ctx, "users", portalParam(c.in, "OWNER_ID")); err != nil {
		return portalOut{}, err
	} else if owner != "" && owner != c.p.UserID {
		if err := s.Matrix.SetOwner(ctx, c.p.UserID, room, owner); err != nil {
			return portalOut{}, err
		}
	}
	n, err := s.portalChatNum(ctx, room)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: n}, s.portalChatBind(c, n)
}

// portalChatBind keeps COLOR, ENTITY_TYPE and ENTITY_ID of a new chat (see migration 876).
func (s *Server) portalChatBind(c portalCall, num int64) error {
	color := strings.ToUpper(strings.TrimSpace(portalStr(portalParam(c.in, "COLOR"))))
	if portalColors[color] == "" {
		color = ""
	}
	et, eid := strings.TrimSpace(portalStr(portalParam(c.in, "ENTITY_TYPE"))), strings.TrimSpace(portalStr(portalParam(c.in, "ENTITY_ID")))
	if color == "" && et == "" {
		return nil
	}
	_, err := s.DB.Exec(c.r.Context(), `UPDATE portal_chats SET color = $2, entity_type = $3, entity_id = $4 WHERE num = $1`,
		num, color, et, eid)
	return err
}

// im.chat.user.add: the employees USERS join the chat CHAT_ID, as its rights let the caller.
func (s *Server) portalChatUserAdd(c portalCall) (portalOut, error) {
	room, err := s.portalChat(c)
	if err != nil {
		return portalOut{}, err
	}
	users, err := s.portalChatPeople(c)
	if err != nil {
		return portalOut{}, err
	}
	if len(users) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "USERS_EMPTY", "err.portal_user")
	}
	err = s.Matrix.AddToGroup(c.r.Context(), c.p.UserID, room, users)
	return portalOut{Result: err == nil}, err
}

// im.chat.user.delete takes the employee USER_ID out of the chat CHAT_ID (the owner or an admin).
func (s *Server) portalChatUserDelete(c portalCall) (portalOut, error) {
	room, err := s.portalChat(c)
	if err != nil {
		return portalOut{}, err
	}
	user, err := s.portalChatPerson(c, portalParam(c.in, "USER_ID"))
	if err != nil {
		return portalOut{}, err
	}
	if user == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "USER_ID_EMPTY", "err.portal_user")
	}
	if user == c.p.UserID { // oneself: leaving takes it
		err = s.Matrix.LeaveGroup(c.r.Context(), c.p.UserID, room)
	} else {
		err = s.Matrix.RemoveFromGroup(c.r.Context(), c.p.UserID, room, user)
	}
	return portalOut{Result: err == nil}, err
}

// im.chat.leave: the caller leaves the chat CHAT_ID.
func (s *Server) portalChatLeave(c portalCall) (portalOut, error) {
	room, err := s.portalChat(c)
	if err != nil {
		return portalOut{}, err
	}
	err = s.Matrix.LeaveGroup(c.r.Context(), c.p.UserID, room)
	return portalOut{Result: err == nil}, err
}

// im.chat.updateTitle: the chat CHAT_ID gets the name TITLE.
func (s *Server) portalChatTitle(c portalCall) (portalOut, error) {
	room, err := s.portalChat(c)
	if err != nil {
		return portalOut{}, err
	}
	title := strings.TrimSpace(portalStr(portalParam(c.in, "TITLE")))
	if title == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "TITLE_EMPTY", "err.portal_title")
	}
	err = s.Matrix.RenameGroup(c.r.Context(), c.p.UserID, room, title)
	return portalOut{Result: err == nil}, err
}

// im.chat.updateAvatar: the chat CHAT_ID gets the picture AVATAR (base64).
func (s *Server) portalChatAvatar(c portalCall) (portalOut, error) {
	room, err := s.portalChat(c)
	if err != nil {
		return portalOut{}, err
	}
	err = s.portalAvatar(c, room)
	return portalOut{Result: err == nil}, err
}

// im.chat.setOwner hands the chat CHAT_ID to the employee USER_ID in it (its owner only).
func (s *Server) portalChatOwner(c portalCall) (portalOut, error) {
	room, err := s.portalChat(c)
	if err != nil {
		return portalOut{}, err
	}
	user, err := s.portalChatPerson(c, portalParam(c.in, "USER_ID"))
	if err != nil {
		return portalOut{}, err
	}
	if user == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "USER_ID_EMPTY", "err.portal_user")
	}
	err = s.Matrix.SetOwner(c.r.Context(), c.p.UserID, room, user)
	return portalOut{Result: err == nil}, err
}

// portalRecent are the caller's chats as im.recent.get gives them, newest first: SKIP_CHAT, SKIP_DIALOG (private
// chats), SKIP_OPENLINES; LAST_UPDATE (LAST_MESSAGE_DATE) — only those with a message after it. A hidden chat is not
// there.
func (s *Server) portalRecent(c portalCall) ([]map[string]any, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return nil, err
	}
	ctx := c.r.Context()
	rooms, err := s.Matrix.Rooms(ctx, c.p.UserID)
	if err != nil {
		return nil, err
	}
	yes := func(name string) bool {
		v := strings.ToUpper(portalStr(portalParam(c.in, name)))
		return v == "Y" || v == "1" || v == "TRUE"
	}
	skipChat, skipDialog := yes("SKIP_CHAT"), yes("SKIP_DIALOG")
	var since time.Time
	for _, name := range []string{"LAST_UPDATE", "LAST_MESSAGE_DATE"} {
		if v := portalStr(portalParam(c.in, name)); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				since = t
			}
		}
	}
	users := map[string]int64{}
	userNum := func(id string) int64 {
		if n, ok := users[id]; ok || id == "" {
			return n
		}
		n, _ := s.portalUserNum(c, id)
		users[id] = n
		return n
	}
	out := []map[string]any{}
	for _, rm := range rooms {
		dm := rm.Kind == "dm" && rm.PeerID != ""
		if rm.Hidden || dm && skipDialog || !dm && skipChat || !since.IsZero() && (rm.Last == nil || !rm.Last.At.After(since)) {
			continue
		}
		num, err := s.portalChatNum(ctx, rm.ID)
		if err != nil {
			return nil, err
		}
		item := map[string]any{"id": "chat" + strconv.FormatInt(num, 10), "chat_id": num, "type": "chat", "title": rm.Name,
			"avatar": map[string]any{"url": "", "color": ""}, "counter": rm.Unread, "pinned": rm.Favorite, "unread": rm.Marked,
			"message": nil, "date_update": nil}
		if dm {
			item["id"], item["type"] = strconv.FormatInt(userNum(rm.PeerID), 10), "user"
		}
		if m := rm.Last; m != nil {
			mn, err := s.portalMessageNum(ctx, rm.ID, m.ID)
			if err != nil {
				return nil, err
			}
			item["message"] = map[string]any{"id": mn, "text": m.Body, "author_id": userNum(m.SenderID), "date": m.At.Format(time.RFC3339)}
			item["date_update"] = m.At.Format(time.RFC3339)
		}
		out = append(out, item)
	}
	return out, nil
}

// im.recent.get: the caller's chats, newest first (see portalRecent).
func (s *Server) portalRecentGet(c portalCall) (portalOut, error) {
	items, err := s.portalRecent(c)
	return portalOut{Result: items}, err
}

// im.recent.list: portalRecent by pages — OFFSET, LIMIT (up to 50) → {items, hasMore}.
func (s *Server) portalRecentList(c portalCall) (portalOut, error) {
	items, err := s.portalRecent(c)
	if err != nil {
		return portalOut{}, err
	}
	offset, _ := strconv.Atoi(portalStr(portalParam(c.in, "OFFSET")))
	limit, _ := strconv.Atoi(portalStr(portalParam(c.in, "LIMIT")))
	if limit <= 0 || limit > portalPage {
		limit = portalPage
	}
	offset = min(max(offset, 0), len(items))
	page := items[offset:min(offset+limit, len(items))]
	return portalOut{Result: map[string]any{"items": page, "hasMore": offset+len(page) < len(items)}}, nil
}

// im.dialog.read reads the chat DIALOG_ID for the caller: up to the message MESSAGE_ID, or all of it.
func (s *Server) portalDialogRead(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	d := portalParam(c.in, "DIALOG_ID")
	room, err := s.portalDialog(c, d)
	if err != nil {
		return portalOut{}, err
	}
	var last int64
	if portalStr(portalParam(c.in, "MESSAGE_ID")) != "" {
		r, event, err := s.portalMessage(c)
		if err == nil && r != room {
			err = i18n.E("err.not_found")
		}
		if err != nil {
			return portalOut{}, err
		}
		last, _ = strconv.ParseInt(portalStr(portalParam(c.in, "MESSAGE_ID")), 10, 64)
		err = s.Matrix.MarkRead(ctx, c.p.UserID, room, event)
	} else {
		err = s.Matrix.MarkRoomRead(ctx, c.p.UserID, room)
	}
	if err != nil {
		return portalOut{}, err
	}
	chat, err := s.portalChatNum(ctx, room)
	return portalOut{Result: map[string]any{"dialogId": strings.TrimSpace(portalStr(d)), "chatId": chat, "lastId": last, "counter": 0}}, err
}

// im.notify.read: the caller's notification ID read with all the older ones (ONLY_CURRENT=Y — it alone).
func (s *Server) portalNotifyRead(c portalCall) (portalOut, error) {
	id, err := strconv.ParseInt(portalStr(portalParam(c.in, "ID")), 10, 64)
	if err != nil || id <= 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ID_EMPTY", "err.portal_notify")
	}
	if strings.ToUpper(portalStr(portalParam(c.in, "ONLY_CURRENT"))) == "Y" {
		err = s.Notify.MarkRead(c.r.Context(), c.p.UserID, id)
	} else {
		_, err = s.DB.Exec(c.r.Context(), `UPDATE notifications SET read_at = now()
			WHERE user_id = $1 AND read_at IS NULL AND id <= $2`, c.p.UserID, id)
	}
	return portalOut{Result: err == nil}, err
}

// im.notify.read.all: all the caller's notifications read.
func (s *Server) portalNotifyReadAll(c portalCall) (portalOut, error) {
	err := s.Notify.MarkRead(c.r.Context(), c.p.UserID, 0)
	return portalOut{Result: err == nil}, err
}

// portalConfirm: NOTIFY_VALUE of the buttons «Y» and «N» → the answer of a go24 confirming notification.
var portalConfirm = map[string]map[string]string{
	"event":              {"Y": "accepted", "N": "declined"},
	"project_invite":     {"Y": "accept", "N": "decline"},
	"group_join_request": {"Y": "accept", "N": "decline"},
}

// im.notify.confirm answers the caller's confirming notification ID with NOTIFY_VALUE — Y (accept) or N (decline),
// or an answer of GET /api/v1/notifications («tentative» for a meeting) — as its buttons in the bell do.
func (s *Server) portalNotifyConfirm(c portalCall) (portalOut, error) {
	id := strings.TrimSpace(portalStr(portalParam(c.in, "ID")))
	if id == "" || id == "0" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ID_EMPTY", "err.portal_notify")
	}
	it, err := s.Notify.Get(c.r.Context(), c.p.UserID, id)
	if errors.Is(err, notify.ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return portalOut{}, i18n.E("err.not_found")
	} else if err != nil {
		return portalOut{}, err
	}
	answer := strings.TrimSpace(portalStr(portalParam(c.in, "NOTIFY_VALUE")))
	if a, ok := portalConfirm[it.Kind][strings.ToUpper(answer)]; ok {
		answer = a
	}
	_, err = s.answerNotify(c.r.Context(), c.p.UserID, id, answer, c.p.Can)
	return portalOut{Result: err == nil}, err
}
