package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"go24/internal/apps"
	"go24/internal/matrixsync"
)

// REST compatible with the portal, chat bots (GO24DEV-925): imbot.register, imbot.unregister, imbot.update,
// imbot.bot.list; imbot.message.add, imbot.message.update, imbot.message.delete; imbot.command.register, update,
// unregister and answer; imbot.chat.sendTyping, imbot.chat.leave. As the go24 REST of bots (apiimbot.go), only an
// application calls them; BOT_ID is the bot's number (none — the application's only bot), COMMAND_ID the command's,
// DIALOG_ID «chatN» or an employee's number — the bot's private chat with them. The handlers bound here hear the bot
// events posts them: a form of PHP arrays with numbers for chats, people, messages and commands.

func init() {
	for name, m := range map[string]portalMethod{
		"imbot.register":           {Scope: "imbot", Run: (*Server).portalBotRegister},
		"imbot.unregister":         {Scope: "imbot", Run: (*Server).portalBotUnregister},
		"imbot.update":             {Scope: "imbot", Run: (*Server).portalBotUpdate},
		"imbot.bot.list":           {Scope: "imbot", Run: (*Server).portalBotList},
		"imbot.message.add":        {Scope: "imbot", Run: (*Server).portalBotMessageAdd},
		"imbot.message.update":     {Scope: "imbot", Run: (*Server).portalBotMessageUpdate},
		"imbot.message.delete":     {Scope: "imbot", Run: (*Server).portalBotMessageDelete},
		"imbot.command.register":   {Scope: "imbot", Run: (*Server).portalCommandRegister},
		"imbot.command.update":     {Scope: "imbot", Run: (*Server).portalCommandUpdate},
		"imbot.command.unregister": {Scope: "imbot", Run: (*Server).portalCommandUnregister},
		"imbot.command.answer":     {Scope: "imbot", Run: (*Server).portalCommandAnswer},
		"imbot.chat.sendTyping":    {Scope: "imbot", Run: (*Server).portalBotTyping},
		"imbot.chat.leave":         {Scope: "imbot", Run: (*Server).portalBotLeave},
	} {
		portalMethods[strings.ToLower(name)] = m
	}
}

// portalBotApp is the application of the token, with the messenger on.
func (s *Server) portalBotApp(c portalCall) (apps.App, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return apps.App{}, err
	}
	if portalV2ByHook(c) {
		if portalV2Token(c) == "" {
			return apps.App{}, s.portalErr(c.r, http.StatusBadRequest, "BOT_TOKEN_NOT_SPECIFIED", "err.imbot_token")
		}
		return s.Apps.WebhookApp(c.r.Context(), c.p.KeyID)
	}
	return s.portalAppOf(c, false)
}

// portalBot is the bot of BOT_ID (its number or code) — or, without it, the application's only bot.
func (s *Server) portalBot(c portalCall) (apps.App, matrixsync.Bot, error) {
	a, err := s.portalBotApp(c)
	if err != nil {
		return a, matrixsync.Bot{}, err
	}
	id := strings.TrimSpace(portalStr(portalParam(c.in, "BOT_ID")))
	if id == "" || id == "0" {
		list, err := s.Matrix.Bots(c.r.Context(), a.ID)
		if err != nil {
			return a, matrixsync.Bot{}, err
		}
		if len(list) != 1 {
			return a, matrixsync.Bot{}, s.portalErr(c.r, http.StatusBadRequest, "BOT_ID_ERROR", "err.not_found")
		}
		return a, list[0], s.portalBotToken(c, list[0])
	}
	b, err := s.Matrix.AppBot(c.r.Context(), a.ID, id)
	if err != nil {
		return a, b, s.portalErr(c.r, http.StatusBadRequest, "BOT_ID_ERROR", "err.not_found")
	}
	return a, b, s.portalBotToken(c, b)
}

// portalBotFields is the bot of imbot.register or of FIELDS of imbot.update: CODE, TYPE, EVENT_HANDLER (all three
// handlers at once) or EVENT_MESSAGE_ADD, EVENT_WELCOME_MESSAGE, EVENT_BOT_DELETE, PROPERTIES (NAME, LAST_NAME,
// WORK_POSITION, COLOR).
func portalBotFields(in map[string]any) matrixsync.Bot {
	var q botReq
	str := func(m map[string]any, k string) string { return strings.TrimSpace(portalStr(portalParam(m, k))) }
	q.Code, q.Type, q.EventHandler = str(in, "CODE"), str(in, "TYPE"), str(in, "EVENT_HANDLER")
	q.MessageAdd, q.Welcome, q.BotDelete = str(in, "EVENT_MESSAGE_ADD"), str(in, "EVENT_WELCOME_MESSAGE"), str(in, "EVENT_BOT_DELETE")
	props := portalMap(portalParam(in, "PROPERTIES"))
	q.Properties.Name, q.Properties.LastName = str(props, "NAME"), str(props, "LAST_NAME")
	q.Properties.WorkPosition, q.Properties.Color = str(props, "WORK_POSITION"), str(props, "COLOR")
	return q.bot()
}

// imbot.register → BOT_ID, the bot's number; people see it in the messenger at once.
func (s *Server) portalBotRegister(c portalCall) (portalOut, error) {
	a, err := s.portalBotApp(c)
	if err != nil {
		return portalOut{}, err
	}
	b := portalBotFields(c.in)
	if b.Code == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "CODE_ERROR", "err.imbot_code_empty")
	}
	if err := s.bindBot(c.r, a, c.p, b, true); err != nil {
		return portalOut{}, err
	}
	if b, err = s.Matrix.RegisterBot(c.r.Context(), a.ID, c.p.UserID, b); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: b.Num}, nil
}

// imbot.unregister: BOT_ID → true; the bot leaves the messenger, its messages stay.
func (s *Server) portalBotUnregister(c portalCall) (portalOut, error) {
	a, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	err = s.Matrix.DeleteBot(c.r.Context(), a.ID, b.ID)
	return portalOut{Result: err == nil}, err
}

// imbot.update: BOT_ID, FIELDS — what is given changes → true.
func (s *Server) portalBotUpdate(c portalCall) (portalOut, error) {
	a, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	ch := portalBotFields(portalMap(portalParam(c.in, "FIELDS")))
	if err := s.bindBot(c.r, a, c.p, ch, true); err != nil {
		return portalOut{}, err
	}
	_, err = s.Matrix.UpdateBot(c.r.Context(), a.ID, b.ID, ch)
	return portalOut{Result: err == nil}, err
}

// imbot.bot.list → {number: {ID, NAME, CODE, OPENLINE, TYPE}}: the application's bots.
func (s *Server) portalBotList(c portalCall) (portalOut, error) {
	a, err := s.portalBotApp(c)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Matrix.Bots(c.r.Context(), a.ID)
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{}
	for _, b := range list {
		out[strconv.FormatInt(b.Num, 10)] = map[string]any{"ID": b.Num, "NAME": b.FullName(), "CODE": b.Code, "OPENLINE": "N", "TYPE": b.Type}
	}
	return portalOut{Result: out}, nil
}

// portalBotDialog is the room of DIALOG_ID for the bot: «chatN» — a chat it is in, an employee's number — its private
// chat with them (started when there is none).
func (s *Server) portalBotDialog(c portalCall, b matrixsync.Bot) (string, error) {
	d := strings.TrimSpace(portalStr(portalParam(c.in, "DIALOG_ID")))
	if d == "" {
		return "", s.portalErr(c.r, http.StatusBadRequest, "DIALOG_ID_EMPTY", "err.portal_dialog")
	}
	if num, ok := strings.CutPrefix(strings.ToLower(d), "chat"); ok {
		return s.portalChatRoom(c.r.Context(), num)
	}
	if strings.HasPrefix(d, "!") {
		return d, nil
	}
	user, err := s.portalNum(c.r.Context(), "users", d)
	if err != nil {
		return "", err
	}
	return s.Matrix.BotDialog(c.r.Context(), b.ID, user)
}

// portalRaw is a parameter as JSON for the bot's message parts: a text that is JSON already stays as it is.
func portalRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	if t, ok := v.(string); ok {
		if t = strings.TrimSpace(t); strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{") {
			return json.RawMessage(t)
		}
	}
	raw, _ := json.Marshal(v)
	return raw
}

// portalBotMsg is MESSAGE, KEYBOARD (a list of buttons or {BUTTONS: […]}), MENU and ATTACH of a bot's message.
func portalBotMsg(in map[string]any) botMsg {
	kb := portalParam(in, "KEYBOARD")
	if m := portalMap(kb); m != nil {
		if list, ok := portalParamOK(m, "BUTTONS"); ok {
			kb = list
		}
	}
	return botMsg{Message: botText(portalStr(portalParam(in, "MESSAGE"))), Keyboard: portalRaw(kb), Menu: portalRaw(portalParam(in, "MENU")),
		Attach: portalRaw(portalParam(in, "ATTACH"))}
}

// imbot.message.add: BOT_ID, DIALOG_ID, MESSAGE, KEYBOARD, MENU, ATTACH → the message's number.
func (s *Server) portalBotMessageAdd(c portalCall) (portalOut, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	m := portalBotMsg(c.in)
	parts, err := m.parts()
	if err != nil {
		return portalOut{}, err
	}
	if strings.TrimSpace(m.Message) == "" && parts.Attach == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "MESSAGE_EMPTY", "err.portal_message")
	}
	room, err := s.portalBotDialog(c, b)
	if err != nil {
		return portalOut{}, err
	}
	msg, err := s.Matrix.BotSend(c.r.Context(), b.ID, room, m.Message, "", parts)
	if err != nil {
		return portalOut{}, err
	}
	n, err := s.portalMessageNum(c.r.Context(), room, msg.ID)
	return portalOut{Result: n}, err
}

// imbot.message.update: BOT_ID, MESSAGE_ID — the bot's own message — and its new MESSAGE, KEYBOARD, MENU, ATTACH → true.
func (s *Server) portalBotMessageUpdate(c portalCall) (portalOut, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	room, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	m := portalBotMsg(c.in)
	parts, err := m.parts()
	if err != nil {
		return portalOut{}, err
	}
	_, err = s.Matrix.BotEdit(c.r.Context(), b.ID, room, event, m.Message, parts)
	return portalOut{Result: err == nil}, err
}

// imbot.message.delete: BOT_ID, MESSAGE_ID → true; the bot takes its message back.
func (s *Server) portalBotMessageDelete(c portalCall) (portalOut, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	room, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	err = s.Matrix.Delete(c.r.Context(), b.ID, room, event)
	return portalOut{Result: err == nil}, err
}

// portalFlagOf is a Y / N parameter; nil — not given.
func portalFlagOf(in map[string]any, k string) *bool {
	v, ok := portalParamOK(in, k)
	if !ok {
		return nil
	}
	t := strings.TrimSpace(portalStr(v))
	y := strings.EqualFold(t, "Y") || t == "1" || strings.EqualFold(t, "true")
	return &y
}

// portalCommandLang is LANG of a command: [{LANGUAGE_ID, TITLE, PARAMS}]; nil — not given.
func portalCommandLang(in map[string]any) map[string]matrixsync.CommandText {
	v, ok := portalParamOK(in, "LANG")
	if !ok {
		return nil
	}
	var q cmdReq
	q.Lang = []cmdLang{}
	var items []map[string]any
	switch x := listify(v).(type) {
	case []any:
		for _, it := range x {
			if m := portalMap(it); m != nil {
				items = append(items, m)
			}
		}
	case map[string]any:
		items = append(items, x)
	}
	for _, l := range items {
		q.Lang = append(q.Lang, cmdLang{LanguageID: portalStr(portalParam(l, "LANGUAGE_ID")), Title: portalStr(portalParam(l, "TITLE")),
			Params: portalStr(portalParam(l, "PARAMS"))})
	}
	return q.lang()
}

// imbot.command.register: BOT_ID, COMMAND, COMMON, HIDDEN, EXTRANET_SUPPORT, EVENT_COMMAND_ADD, LANG → COMMAND_ID.
func (s *Server) portalCommandRegister(c portalCall) (portalOut, error) {
	a, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	handler := strings.TrimSpace(portalStr(portalParam(c.in, "EVENT_COMMAND_ADD")))
	if handler == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "EVENT_COMMAND_ADD", "err.imbot_command_handler")
	}
	if err := s.bindCommand(c.r, a, c.p, handler, true); err != nil {
		return portalOut{}, err
	}
	cmd := matrixsync.Command{Command: strings.TrimPrefix(strings.TrimSpace(portalStr(portalParam(c.in, "COMMAND"))), "/"),
		Lang: portalCommandLang(c.in), Handler: handler}
	for _, f := range []struct {
		to *bool
		k  string
	}{{&cmd.Common, "COMMON"}, {&cmd.Hidden, "HIDDEN"}, {&cmd.Extranet, "EXTRANET_SUPPORT"}} {
		if v := portalFlagOf(c.in, f.k); v != nil {
			*f.to = *v
		}
	}
	if cmd, err = s.Matrix.RegisterCommand(c.r.Context(), a.ID, b.ID, cmd); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: cmd.Num}, nil
}

// portalBotCommand is the command of COMMAND_ID — or of COMMAND, the name, of the bot BOT_ID.
func (s *Server) portalBotCommand(c portalCall) (apps.App, matrixsync.Command, error) {
	if id := strings.TrimSpace(portalStr(portalParam(c.in, "COMMAND_ID"))); id != "" && id != "0" {
		a, err := s.portalBotApp(c)
		if err != nil {
			return a, matrixsync.Command{}, err
		}
		cmd, err := s.Matrix.CommandOf(c.r.Context(), a.ID, id)
		if err != nil {
			return a, cmd, s.portalErr(c.r, http.StatusBadRequest, "COMMAND_ID_ERROR", "err.not_found")
		}
		return a, cmd, nil
	}
	a, b, err := s.portalBot(c)
	if err != nil {
		return a, matrixsync.Command{}, err
	}
	cmd, err := s.Matrix.BotCommand(c.r.Context(), a.ID, b.ID, portalStr(portalParam(c.in, "COMMAND")))
	if err != nil {
		return a, cmd, s.portalErr(c.r, http.StatusBadRequest, "COMMAND_ID_ERROR", "err.not_found")
	}
	return a, cmd, nil
}

// imbot.command.update: COMMAND_ID, FIELDS (EVENT_COMMAND_ADD, COMMON, HIDDEN, EXTRANET_SUPPORT, LANG) → true.
func (s *Server) portalCommandUpdate(c portalCall) (portalOut, error) {
	a, cmd, err := s.portalBotCommand(c)
	if err != nil {
		return portalOut{}, err
	}
	f := portalMap(portalParam(c.in, "FIELDS"))
	handler := strings.TrimSpace(portalStr(portalParam(f, "EVENT_COMMAND_ADD")))
	if err := s.bindCommand(c.r, a, c.p, handler, true); err != nil {
		return portalOut{}, err
	}
	_, err = s.Matrix.UpdateCommand(c.r.Context(), a.ID, cmd.BotID, cmd.ID, matrixsync.CommandChange{Common: portalFlagOf(f, "COMMON"),
		Hidden: portalFlagOf(f, "HIDDEN"), Extranet: portalFlagOf(f, "EXTRANET_SUPPORT"), Lang: portalCommandLang(f), Handler: handler})
	return portalOut{Result: err == nil}, err
}

// imbot.command.unregister: COMMAND_ID → true.
func (s *Server) portalCommandUnregister(c portalCall) (portalOut, error) {
	a, cmd, err := s.portalBotCommand(c)
	if err != nil {
		return portalOut{}, err
	}
	err = s.Matrix.DeleteCommand(c.r.Context(), a.ID, cmd.BotID, cmd.ID)
	return portalOut{Result: err == nil}, err
}

// imbot.command.answer: COMMAND_ID (or COMMAND and BOT_ID), MESSAGE_ID — the command's message, of its event —
// MESSAGE, KEYBOARD, MENU, ATTACH → the number of the bot's answer, in the command's chat (and thread).
func (s *Server) portalCommandAnswer(c portalCall) (portalOut, error) {
	a, cmd, err := s.portalBotCommand(c)
	if err != nil {
		return portalOut{}, err
	}
	m := portalBotMsg(c.in)
	parts, err := m.parts()
	if err != nil {
		return portalOut{}, err
	}
	_, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	room, thread, err := s.Matrix.CommandRoom(c.r.Context(), a.ID, cmd, event)
	if err != nil {
		return portalOut{}, err
	}
	msg, err := s.Matrix.BotSend(c.r.Context(), cmd.BotID, room, m.Message, thread, parts)
	if err != nil {
		return portalOut{}, err
	}
	n, err := s.portalMessageNum(c.r.Context(), room, msg.ID)
	return portalOut{Result: n}, err
}

// imbot.chat.sendTyping: BOT_ID, DIALOG_ID → true; the others see «typing…» of the bot.
func (s *Server) portalBotTyping(c portalCall) (portalOut, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	room, err := s.portalBotDialog(c, b)
	if err != nil {
		return portalOut{}, err
	}
	err = s.Matrix.BotTyping(c.r.Context(), b.ID, room)
	return portalOut{Result: err == nil}, err
}

// imbot.chat.leave: BOT_ID, CHAT_ID → true; the bot leaves the chat.
func (s *Server) portalBotLeave(c portalCall) (portalOut, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	chat := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(portalStr(portalParam(c.in, "CHAT_ID")))), "chat")
	if chat == "" || chat == "0" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "CHAT_ID_EMPTY", "err.portal_chat")
	}
	room, err := s.portalChatRoom(c.r.Context(), chat)
	if err != nil {
		return portalOut{}, err
	}
	err = s.Matrix.BotLeave(c.r.Context(), b.ID, room)
	return portalOut{Result: err == nil}, err
}
