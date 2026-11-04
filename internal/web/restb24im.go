package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/msgfmt"
)

// REST compatible with the portal, the messenger (GO24DEV-839): im.notify.system.add, im.notify.personal.add (and the
// old im.notify — a personal one), im.notify.delete; im.message.add, im.message.update, im.message.delete;
// im.dialog.get (im.chat.get), im.chat.user.list, im.dialog.messages.get. DIALOG_ID is «chatN» — a chat by its number —
// or an employee's number — the private chat with them, started when there is none. Chats and messages of Matrix get
// numbers when an integration first meets them (migration 665); the messages go as the caller writes them, with the
// BB codes.

func init() {
	for name, m := range map[string]portalMethod{
		"im.notify":              {Scope: "im:write", Run: (*Server).portalNotifyPersonal},
		"im.notify.personal.add": {Scope: "im:write", Run: (*Server).portalNotifyPersonal},
		"im.notify.system.add":   {Scope: "im:write", Run: (*Server).portalNotifySystem},
		"im.notify.delete":       {Scope: "im:write", Run: (*Server).portalNotifyDelete},
		"im.message.add":         {Scope: "im:write", Run: (*Server).portalMessageAdd},
		"im.message.update":      {Scope: "im:write", Run: (*Server).portalMessageUpdate},
		"im.message.delete":      {Scope: "im:write", Run: (*Server).portalMessageDelete},
		"im.dialog.get":          {Scope: "im:read", Run: (*Server).portalDialogGet},
		"im.chat.get":            {Scope: "im:read", Run: (*Server).portalDialogGet},
		"im.chat.user.list":      {Scope: "im:read", Run: (*Server).portalChatUsers},
		"im.dialog.messages.get": {Scope: "im:read", Run: (*Server).portalDialogMessages},
	} {
		portalMethods[name] = m
	}
}

func (s *Server) portalNotifyPersonal(c portalCall) (portalOut, error) { return s.portalNotify(c, "app_personal") }
func (s *Server) portalNotifySystem(c portalCall) (portalOut, error)   { return s.portalNotify(c, "app") }

// portalNotify: the bell of the employee USER_ID gets MESSAGE (its BB codes dropped) — «app» without an author, as a
// system notification, «app_personal» from the caller. One with the same TAG replaces the caller's earlier
// one. The number of the notification; false — the employee turned such notifications off.
func (s *Server) portalNotify(c portalCall, kind string) (portalOut, error) {
	ctx := c.r.Context()
	user, err := s.portalNum(ctx, "users", portalParam(c.in, "USER_ID"))
	if err != nil {
		return portalOut{}, err
	}
	if user == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "USER_ID_EMPTY", "err.portal_user")
	}
	text := strings.TrimSpace(msgfmt.Plain(botText(portalStr(portalParam(c.in, "MESSAGE")))))
	if text == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "MESSAGE_EMPTY", "err.portal_message")
	}
	var actor *string
	if kind == "app_personal" {
		actor = &c.p.UserID
	}
	tag := strings.TrimSpace(portalStr(portalParam(c.in, "TAG")))
	var id int64
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if tag != "" {
			if _, err := tx.Exec(ctx, `DELETE FROM notifications WHERE user_id = $1 AND sent_by = $2 AND tag = $3`, user, c.p.UserID, tag); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, sent_by, tag)
			VALUES ($1, $2, $3, left($4, 1000), $5, nullif($6, '')) RETURNING id`, user, actor, kind, text, c.p.UserID, tag).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) { // turned off (notifications_filter)
		return portalOut{Result: false}, nil
	}
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: id}, nil
}

// im.notify.delete: the caller's notification by ID, or all of theirs with TAG (SUB_TAG is taken as a tag too).
func (s *Server) portalNotifyDelete(c portalCall) (portalOut, error) {
	id, tag := portalStr(portalParam(c.in, "ID")), portalStr(portalParam(c.in, "TAG"))
	if tag == "" {
		tag = portalStr(portalParam(c.in, "SUB_TAG"))
	}
	if id == "" && tag == "" {
		return portalOut{}, i18n.E("err.portal_id")
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	res, err := s.DB.Exec(c.r.Context(), `DELETE FROM notifications WHERE sent_by = $1 AND (id = $2 OR tag = nullif($3, ''))`, c.p.UserID, n, tag)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: res.RowsAffected() > 0}, nil
}

// portalDialog is the room of DIALOG_ID: «chatN» (or a room id of Matrix) — the chat, an employee's (or a bot's) number — the private
// chat with them.
func (s *Server) portalDialog(c portalCall, v any) (string, error) {
	ctx := c.r.Context()
	d := strings.TrimSpace(portalStr(v))
	if d == "" {
		return "", s.portalErr(c.r, http.StatusBadRequest, "DIALOG_ID_EMPTY", "err.portal_dialog")
	}
	if strings.HasPrefix(d, "!") {
		return d, nil
	}
	if num, ok := strings.CutPrefix(strings.ToLower(d), "chat"); ok {
		return s.portalChatRoom(ctx, num)
	}
	if s.Matrix == nil {
		return "", i18n.E("err.not_found")
	}
	user, err := s.portalNum(ctx, "users", d)
	if n, nerr := strconv.ParseInt(d, 10, 64); err != nil && nerr == nil { // a bot's number: the private chat with it
		if b, berr := s.Matrix.LiveBot(ctx, n); berr == nil {
			user, err = b.ID, nil
		}
	}
	if err != nil {
		return "", err
	}
	room, err := s.Matrix.DirectChat(ctx, c.p.UserID, user)
	if errors.Is(err, matrixsync.ErrNoAccount) {
		return "", i18n.E("err.not_found")
	}
	return room, err
}

// portalChatRoom is the room of the chat number num.
func (s *Server) portalChatRoom(ctx context.Context, num string) (string, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
	if err != nil {
		return "", i18n.E("err.not_found")
	}
	var room string
	err = s.DB.QueryRow(ctx, `SELECT room_id FROM portal_chats WHERE num = $1`, n).Scan(&room)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", i18n.E("err.not_found")
	}
	return room, err
}

// portalChatNum is the number of a Matrix room, given when an integration first meets it.
func (s *Server) portalChatNum(ctx context.Context, room string) (int64, error) { return s.Matrix.PortalChatNum(ctx, room) }

// portalMessageNum is the number of a message.
func (s *Server) portalMessageNum(ctx context.Context, room, event string) (int64, error) {
	return s.Matrix.PortalMessageNum(ctx, room, event)
}

// portalMessage is the room and event of MESSAGE_ID.
func (s *Server) portalMessage(c portalCall) (string, string, error) {
	n, err := strconv.ParseInt(portalStr(portalParam(c.in, "MESSAGE_ID")), 10, 64)
	if err != nil {
		return "", "", s.portalErr(c.r, http.StatusBadRequest, "MESSAGE_ID_EMPTY", "err.portal_message_id")
	}
	var room, event string
	err = s.DB.QueryRow(c.r.Context(), `SELECT room_id, event_id FROM portal_messages WHERE num = $1`, n).Scan(&room, &event)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", i18n.E("err.not_found")
	}
	return room, event, err
}

func (s *Server) portalNeedMatrix(c portalCall) error {
	if s.Matrix == nil {
		return s.portalErr(c.r, http.StatusNotFound, "ERROR_METHOD_NOT_FOUND", "err.portal_method", "im")
	}
	return nil
}

// im.message.add: MESSAGE to DIALOG_ID as the caller writes it; the message's number.
func (s *Server) portalMessageAdd(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	text := strings.TrimSpace(botText(portalStr(portalParam(c.in, "MESSAGE"))))
	if text == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "MESSAGE_EMPTY", "err.portal_message")
	}
	room, err := s.portalDialog(c, portalParam(c.in, "DIALOG_ID"))
	if err != nil {
		return portalOut{}, err
	}
	m, err := s.Matrix.Send(c.r.Context(), c.p.UserID, room, text, "")
	if err != nil {
		return portalOut{}, err
	}
	n, err := s.portalMessageNum(c.r.Context(), room, m.ID)
	return portalOut{Result: n}, err
}

// im.message.update: a new MESSAGE of the caller's own message MESSAGE_ID (an empty one takes it back).
func (s *Server) portalMessageUpdate(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	room, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	text := strings.TrimSpace(botText(portalStr(portalParam(c.in, "MESSAGE"))))
	if text == "" {
		err = s.Matrix.Delete(c.r.Context(), c.p.UserID, room, event)
	} else {
		_, err = s.Matrix.Edit(c.r.Context(), c.p.UserID, room, event, text)
	}
	return portalOut{Result: err == nil}, err
}

// im.message.delete takes back the caller's own message MESSAGE_ID.
func (s *Server) portalMessageDelete(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	room, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	err = s.Matrix.Delete(c.r.Context(), c.p.UserID, room, event)
	return portalOut{Result: err == nil}, err
}

// portalChatTypes: the kind of a go24 chat → the TYPE and ENTITY_TYPE.
var portalChatTypes = map[string][2]string{
	"dm": {"private", ""}, "notes": {"private", ""}, "group": {"chat", ""}, "channel": {"channel", ""},
	"task": {"chat", "TASKS"}, "project": {"chat", "SONET_GROUP"}, "collab": {"chat", "SONET_GROUP"}, "feed": {"chat", "LIVECHAT"},
}

// portalChatOf is a chat the caller is in, as im.dialog.get gives it.
func (s *Server) portalChatOf(c portalCall, room string) (map[string]any, error) {
	ctx := c.r.Context()
	rooms, err := s.Matrix.Rooms(ctx, c.p.UserID)
	if err != nil {
		return nil, err
	}
	var rm *matrixsync.Room
	for i := range rooms {
		if rooms[i].ID == room {
			rm = &rooms[i]
		}
	}
	if rm == nil {
		return nil, i18n.E("err.not_found")
	}
	num, err := s.portalChatNum(ctx, room)
	if err != nil {
		return nil, err
	}
	var owner int64
	if mem, err := s.Matrix.ChatMembers(ctx, c.p.UserID, room); err == nil {
		for _, m := range mem {
			if m.Owner && m.ID != "" {
				owner, _ = s.portalUserNum(c, m.ID)
			}
		}
	}
	typ := portalChatTypes[rm.Kind]
	if typ[0] == "" {
		typ[0] = "chat"
	}
	out := map[string]any{"id": num, "dialog_id": "chat" + strconv.FormatInt(num, 10), "name": rm.Name, "owner": owner,
		"type": typ[0], "entity_type": typ[1], "entity_id": "", "extranet": false, "avatar": "", "color": "",
		"message_count": nil, "unread_count": rm.Unread}
	if rm.Kind == "dm" && rm.PeerID != "" {
		if n, err := s.portalUserNum(c, rm.PeerID); err == nil {
			out["entity_id"] = strconv.FormatInt(n, 10)
		}
	}
	var color, et, eid string
	if err := s.DB.QueryRow(ctx, `SELECT color, entity_type, entity_id FROM portal_chats WHERE num = $1`, num).Scan(&color, &et, &eid); err != nil {
		return nil, err
	}
	out["color"] = portalColors[color]
	if et != "" {
		out["entity_type"], out["entity_id"] = et, eid
	}
	return out, nil
}

// im.dialog.get (im.chat.get): the chat of DIALOG_ID (or CHAT_ID).
func (s *Server) portalDialogGet(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	if et := strings.TrimSpace(portalStr(portalParam(c.in, "ENTITY_TYPE"))); strings.EqualFold(et, "CRM") {
		return s.portalCRMChatGet(c)
	} else if et != "" {
		return s.portalEntityChat(c, et)
	}
	d := portalParam(c.in, "DIALOG_ID")
	if chat := portalStr(portalParam(c.in, "CHAT_ID")); chat != "" && portalStr(d) == "" {
		d = "chat" + chat
	}
	room, err := s.portalDialog(c, d)
	if err != nil {
		return portalOut{}, err
	}
	chat, err := s.portalChatOf(c, room)
	return portalOut{Result: chat}, err
}

// portalEntityChat: im.chat.get (imbot.chat.get) with ENTITY_TYPE and ENTITY_ID of im.chat.add → {"ID": the number} of
// the newest such chat the caller is in, null — none.
func (s *Server) portalEntityChat(c portalCall, et string) (portalOut, error) {
	ctx := c.r.Context()
	rows, err := s.DB.Query(ctx, `SELECT num, room_id FROM portal_chats WHERE entity_type = $1 AND entity_id = $2 ORDER BY num DESC`,
		et, strings.TrimSpace(portalStr(portalParam(c.in, "ENTITY_ID"))))
	if err != nil {
		return portalOut{}, err
	}
	type chat struct {
		num  int64
		room string
	}
	var list []chat
	for rows.Next() {
		var x chat
		if err := rows.Scan(&x.num, &x.room); err != nil {
			rows.Close()
			return portalOut{}, err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return portalOut{}, err
	}
	for _, x := range list {
		if _, err := s.Matrix.ChatMembers(ctx, c.p.UserID, x.room); err == nil {
			return portalOut{Result: map[string]any{"ID": x.num}}, nil
		}
	}
	return portalOut{Result: nil}, nil
}

// im.chat.get with ENTITY_TYPE CRM and ENTITY_ID «DEAL|12» (LEAD, CONTACT, COMPANY): {"ID": the number} of the
// card's «Чат», null — the card has none (the caller must see the card).
func (s *Server) portalCRMChatGet(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	kind, num, _ := strings.Cut(strings.TrimSpace(portalStr(portalParam(c.in, "ENTITY_ID"))), "|")
	entity := strings.ToLower(kind)
	table := map[string]string{"deal": "crm_deals", "lead": "crm_leads", "contact": "crm_contacts", "company": "crm_companies"}[entity]
	if table == "" || strings.TrimSpace(num) == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ENTITY_ID_EMPTY", "err.not_found")
	}
	id, err := s.portalNum(ctx, table, num)
	if err != nil {
		return portalOut{}, err
	}
	resp, err := s.CRM.Responsible(ctx, entity, id)
	if err == nil {
		err = s.CRM.CheckCard(ctx, c.p.UserID, entity, "read", id, resp)
	}
	if err != nil {
		return portalOut{}, err
	}
	room, err := s.CRM.ChatOf(ctx, entity, id)
	if err != nil || room == "" {
		return portalOut{Result: nil}, err
	}
	n, err := s.portalChatNum(ctx, room)
	return portalOut{Result: map[string]any{"ID": n}}, err
}

// im.chat.user.list: the numbers of the employees in the chat CHAT_ID.
func (s *Server) portalChatUsers(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	room, err := s.portalDialog(c, "chat"+portalStr(portalParam(c.in, "CHAT_ID")))
	if err != nil {
		return portalOut{}, err
	}
	mem, err := s.Matrix.ChatMembers(c.r.Context(), c.p.UserID, room)
	if err != nil {
		return portalOut{}, err
	}
	out := []int64{}
	for _, m := range mem {
		if m.ID == "" {
			continue
		}
		if n, err := s.portalUserNum(c, m.ID); err == nil {
			out = append(out, n)
		}
	}
	return portalOut{Result: out}, nil
}

// portalPages is how many pages of a chat im.dialog.messages.get looks through for LAST_ID or FIRST_ID.
const portalPages = 20

// im.dialog.messages.get: the messages of DIALOG_ID, newest first — LIMIT (up to 50) before LAST_ID, or, with
// FIRST_ID, the ones after it, oldest first; with the authors in «users».
func (s *Server) portalDialogMessages(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	room, err := s.portalDialog(c, portalParam(c.in, "DIALOG_ID"))
	if err != nil {
		return portalOut{}, err
	}
	limit, _ := strconv.Atoi(portalStr(portalParam(c.in, "LIMIT")))
	if limit <= 0 || limit > portalPage {
		limit = portalPage
	}
	// the message LAST_ID or FIRST_ID is looked for by its event
	mark, after := portalStr(portalParam(c.in, "LAST_ID")), false
	if f := portalStr(portalParam(c.in, "FIRST_ID")); f != "" && f != "0" {
		mark, after = f, true
	}
	event := ""
	if mark != "" && mark != "0" {
		var r string
		err := s.DB.QueryRow(ctx, `SELECT room_id, event_id FROM portal_messages WHERE num = $1`, mark).Scan(&r, &event)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && r != room {
			return portalOut{}, i18n.E("err.not_found")
		} else if err != nil {
			return portalOut{}, err
		}
	}
	// pages of the timeline, oldest first, until the mark is met (or there are enough)
	var msgs []matrixsync.Message
	from, found := "", event == ""
	for page := 0; page < portalPages; page++ {
		list, prev, err := s.Matrix.Messages(ctx, c.p.UserID, room, from)
		if err != nil {
			return portalOut{}, err
		}
		msgs = append(portalSaid(list), msgs...)
		found = event == "" || portalBefore(msgs, event) < len(msgs)
		from = prev
		if prev == "" || found && (after || portalBefore(msgs, event) >= limit) {
			break
		}
	}
	if !found {
		return portalOut{}, i18n.E("err.not_found")
	}
	var pick []matrixsync.Message // oldest first
	i := len(msgs)
	if event != "" {
		i = portalBefore(msgs, event)
	}
	if after {
		pick = msgs[min(i+1, len(msgs)):]
		pick = pick[:min(limit, len(pick))]
	} else {
		pick = msgs[max(0, i-limit):i]
	}
	chat, err := s.portalChatNum(ctx, room)
	if err != nil {
		return portalOut{}, err
	}
	out, users := []map[string]any{}, []map[string]any{}
	seen := map[string]int64{}
	for _, m := range pick {
		num, err := s.portalMessageNum(ctx, room, m.ID)
		if err != nil {
			return portalOut{}, err
		}
		author, ok := seen[m.SenderID]
		if !ok && m.SenderID != "" {
			dialogUser := s.portalDialogUser
			if m.SenderBot {
				dialogUser = s.portalDialogBot
			}
			u, n, err := dialogUser(c, m.SenderID)
			if err != nil {
				return portalOut{}, err
			}
			author = n
			users = append(users, u)
		}
		seen[m.SenderID] = author
		params := map[string]any{}
		if m.File != nil {
			params["FILE_NAME"] = m.File.Name
		}
		out = append(out, map[string]any{"id": num, "chat_id": chat, "author_id": author, "date": m.At.Format(time.RFC3339),
			"text": m.Body, "params": params})
	}
	if !after { // newest first gives them
		for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
			out[l], out[r] = out[r], out[l]
		}
	}
	return portalOut{Result: map[string]any{"chat_id": chat, "messages": out, "users": users, "files": []any{}}}, nil
}

// portalSaid are the messages people wrote (not the lines about the chat itself).
func portalSaid(list []matrixsync.Message) []matrixsync.Message {
	out := make([]matrixsync.Message, 0, len(list))
	for _, m := range list {
		if m.Event == "" && m.Change == "" {
			out = append(out, m)
		}
	}
	return out
}

// portalBefore is the position of the message event in msgs (len — not there).
func portalBefore(msgs []matrixsync.Message, event string) int {
	for i, m := range msgs {
		if m.ID == event {
			return i
		}
	}
	return len(msgs)
}

// portalDialogBot is a chat bot among the authors of im.dialog.messages.get, by its number (BOT_ID).
func (s *Server) portalDialogBot(c portalCall, id string) (map[string]any, int64, error) {
	var num int64
	var first, last, position string
	err := s.DB.QueryRow(c.r.Context(), `SELECT num, name, last_name, work_position FROM im_bots WHERE id::text = $1`, id).
		Scan(&num, &first, &last, &position)
	if err != nil {
		return nil, 0, err
	}
	return map[string]any{"id": num, "name": strings.TrimSpace(first + " " + last), "first_name": first, "last_name": last,
		"work_position": position, "gender": "", "avatar": "", "extranet": false, "active": true, "bot": true}, num, nil
}

// portalDialogUser is an author of im.dialog.messages.get and their number.
func (s *Server) portalDialogUser(c portalCall, id string) (map[string]any, int64, error) {
	prof, err := s.Company.Profile(c.r.Context(), id)
	if err != nil {
		return nil, 0, err
	}
	num, err := s.portalUserNum(c, id)
	if err != nil {
		return nil, 0, err
	}
	first, last := prof.FirstName, prof.LastName
	if first == "" && last == "" {
		first, last = prof.EditFirst(), prof.EditLast()
	}
	return map[string]any{"id": num, "name": strings.TrimSpace(first + " " + last), "first_name": first, "last_name": last,
		"work_position": prof.Position, "gender": prof.Gender, "avatar": s.portalPhoto(prof), "extranet": prof.Guest,
		"active": prof.Active}, num, nil
}
