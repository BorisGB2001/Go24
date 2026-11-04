package web

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"

	"go24/internal/apps"
	"go24/internal/matrixsync"
)

// REST compatible with the portal, chat bots 2.0 (GO24DEV-954): imbot.v2.Bot.register, update, get, list, unregister;
// imbot.v2.Event.get; imbot.v2.Chat.Message.send, update, delete, get; imbot.v2.Chat.InputAction.notify;
// imbot.v2.Command.register, update, unregister, list, answer; imbot.v2.Revision.get; those of the bot's chats are in
// restb24imbotv2chat.go. The bot is the one of imbot.* (a Matrix account, apiimbot.go), its events are those of
// imbot.v2 (matrixsync/botsv2.go): taken by Event.get (eventMode «fetch») or posted to its webhookUrl (eventMode
// «webhook»). Parameters and answers are in camelCase with numbers: botId, dialogId («chatN» or a person's number),
// messageId, commandId; the fields in «fields».

func init() {
	for name, run := range map[string]func(*Server, portalCall) (portalOut, error){
		"imbot.v2.Bot.register":                 (*Server).portalV2BotRegister,
		"imbot.v2.Bot.update":                   (*Server).portalV2BotUpdate,
		"imbot.v2.Bot.get":                      (*Server).portalV2BotGet,
		"imbot.v2.Bot.list":                     (*Server).portalV2BotList,
		"imbot.v2.Bot.unregister":               (*Server).portalV2BotUnregister,
		"imbot.v2.Event.get":                    (*Server).portalV2EventGet,
		"imbot.v2.Chat.Message.send":            (*Server).portalV2MessageSend,
		"imbot.v2.Chat.Message.update":          (*Server).portalV2MessageUpdate,
		"imbot.v2.Chat.Message.delete":          (*Server).portalV2MessageDelete,
		"imbot.v2.Chat.Message.get":             (*Server).portalV2MessageGet,
		"imbot.v2.Chat.InputAction.notify":      (*Server).portalV2Typing,
		"imbot.v2.Chat.add":                     (*Server).portalV2ChatAdd,
		"imbot.v2.Chat.get":                     (*Server).portalV2ChatGet,
		"imbot.v2.Chat.update":                  (*Server).portalV2ChatUpdate,
		"imbot.v2.Chat.User.list":               (*Server).portalV2ChatUsers,
		"imbot.v2.Chat.Message.read":            (*Server).portalV2Read,
		"imbot.v2.Chat.Message.getContext":      (*Server).portalV2Context,
		"imbot.v2.Chat.Message.Reaction.add":    (*Server).portalV2ReactionAdd,
		"imbot.v2.Chat.Message.Reaction.delete": (*Server).portalV2ReactionDelete,
		"imbot.v2.Chat.TextField.enabled":       (*Server).portalV2TextField,
		"imbot.v2.Command.register":             (*Server).portalV2CommandRegister,
		"imbot.v2.Command.update":               (*Server).portalV2CommandUpdate,
		"imbot.v2.Command.unregister":           (*Server).portalV2CommandUnregister,
		"imbot.v2.Command.list":                 (*Server).portalV2CommandList,
		"imbot.v2.Command.answer":               (*Server).portalV2CommandAnswer,
	} {
		run := run
		portalMethods[strings.ToLower(name)] = portalMethod{Scope: "imbot", Run: func(s *Server, c portalCall) (portalOut, error) {
			return run(s, portalV2(c))
		}}
	}
	portalMethods["imbot.v2.revision.get"] = portalMethod{Scope: "imbot", Run: (*Server).portalIMRevision}
}

// portalV2 is the call with the parameters of imbot.v2 under the names of imbot as well (botId — BOT_ID …), so that the
// helpers of imbot.* read them.
func portalV2(c portalCall) portalCall {
	in := make(map[string]any, len(c.in)+4)
	for k, v := range c.in {
		in[k] = v
	}
	for _, k := range [][2]string{{"botId", "BOT_ID"}, {"dialogId", "DIALOG_ID"}, {"messageId", "MESSAGE_ID"}, {"commandId", "COMMAND_ID"}} {
		if v, ok := c.in[k[0]]; ok {
			in[k[1]] = v
		}
	}
	c.in = in
	return c
}

// portalV2ByHook: an incoming webhook calls imbot.v2 — as the bot of botToken, not as an application (GO24DEV-962); the bots
// it registers are of its own application (apps.WebhookApp).
func portalV2ByHook(c portalCall) bool {
	return strings.HasPrefix(c.method, "imbot.v2.") && !strings.HasPrefix(c.p.KeyID, "app:")
}

// portalV2Token is botToken of the call — or, at Bot.register, of its fields.
func portalV2Token(c portalCall) string {
	if t := portalV2Str(c.in, "botToken"); t != "" {
		return t
	}
	return portalV2Str(portalMap(portalParam(c.in, "fields")), "botToken")
}

// portalBotToken checks botToken of a webhook's call against the one the bot b was registered with.
func (s *Server) portalBotToken(c portalCall, b matrixsync.Bot) error {
	if !portalV2ByHook(c) {
		return nil
	}
	sum := matrixsync.HashBotToken(portalV2Token(c))
	if b.BotToken == "" || subtle.ConstantTimeCompare([]byte(sum), []byte(b.BotToken)) != 1 {
		return s.portalErr(c.r, http.StatusUnauthorized, "BOT_OWNERSHIP_ERROR", "err.imbot_token_wrong")
	}
	return nil
}

// portalV2Fields is «fields» of the call; without it — the call itself (the flat form of earlier imbot.v2).
func portalV2Fields(c portalCall) map[string]any {
	if f := portalMap(portalParam(c.in, "fields")); f != nil {
		return f
	}
	return c.in
}

// portalV2Str is a text parameter, trimmed.
func portalV2Str(in map[string]any, k string) string {
	return strings.TrimSpace(portalStr(portalParam(in, k)))
}

// portalV2Bot is the answer about a bot: {bot, users}.
func (s *Server) portalV2Bot(c portalCall, b matrixsync.Bot) (portalOut, error) {
	bot, err := s.Matrix.BotV2(c.r.Context(), b)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"bot": bot, "users": []any{matrixsync.BotV2User(b)}}}, nil
}

// portalV2Hook subscribes webhookUrl of a «webhook» bot to the events of imbot.v2.
func (s *Server) portalV2Hook(c portalCall, a apps.App, b matrixsync.Bot) error {
	if b.EventMode != "webhook" {
		return nil
	}
	if b.WebhookURL == "" {
		return s.portalErr(c.r, http.StatusBadRequest, "BOT_WEBHOOK_URL_REQUIRED", "err.imbot_webhook")
	}
	for _, ev := range matrixsync.BotV2Events {
		if _, err := s.Apps.BindEvent(c.r.Context(), a, apps.Event{Event: ev, Handler: b.WebhookURL, UserID: c.p.UserID, Portal: true}); err != nil {
			return err
		}
	}
	return nil
}

// portalV2Props puts properties (name, lastName, workPosition, color) on the bot.
func portalV2Props(f map[string]any, b *matrixsync.Bot) {
	p := portalMap(portalParam(f, "properties"))
	b.Name, b.LastName = portalV2Str(p, "name"), portalV2Str(p, "lastName")
	b.WorkPosition, b.Color = portalV2Str(p, "workPosition"), portalV2Str(p, "color")
}

// portalV2Change is what fields of Bot.register or Bot.update set of imbot.v2; mode — eventMode given.
func (s *Server) portalV2Change(c portalCall, f map[string]any) (matrixsync.BotV2Change, error) {
	var ch matrixsync.BotV2Change
	str := func(k string) *string {
		if v, ok := portalParamOK(f, k); ok {
			t := strings.TrimSpace(portalStr(v))
			return &t
		}
		return nil
	}
	ch.EventMode, ch.WebhookURL, ch.BotToken, ch.Background = str("eventMode"), str("webhookUrl"), str("botToken"), str("backgroundId")
	if ch.EventMode != nil {
		if *ch.EventMode = strings.ToLower(*ch.EventMode); *ch.EventMode != "fetch" && *ch.EventMode != "webhook" {
			return ch, s.portalErr(c.r, http.StatusBadRequest, "BOT_INVALID_EVENT_MODE", "err.imbot_event_mode", *ch.EventMode)
		}
	}
	ch.Hidden, ch.Reactions, ch.Openline = portalFlagOf(f, "isHidden"), portalFlagOf(f, "isReactionsEnabled"), portalFlagOf(f, "isSupportOpenline")
	return ch, nil
}

// imbot.v2.Bot.register: fields — code, botToken, properties, type (bot, supervisor, personal, openline), eventMode
// (fetch — by default, webhook with webhookUrl), isHidden, isReactionsEnabled, isSupportOpenline, backgroundId →
// {bot, users}. The same code again gives the bot there is.
func (s *Server) portalV2BotRegister(c portalCall) (portalOut, error) {
	a, err := s.portalBotApp(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	f := portalV2Fields(c)
	code := portalV2Str(f, "code")
	if code == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "BOT_CODE_REQUIRED", "err.imbot_code_empty")
	}
	if b, err := s.Matrix.AppBot(ctx, a.ID, code); err == nil && strings.EqualFold(b.Code, code) {
		if err := s.portalBotToken(c, b); err != nil {
			return portalOut{}, err
		}
		return s.portalV2Bot(c, b)
	}
	typ, ok := matrixsync.BotV2Type(portalV2Str(f, "type"))
	if !ok {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "BOT_INVALID_TYPE", "err.imbot_v2_type", portalV2Str(f, "type"))
	}
	ch, err := s.portalV2Change(c, f)
	if err != nil {
		return portalOut{}, err
	}
	b := matrixsync.Bot{Code: code, Type: typ, EventMode: "fetch", Reactions: true,
		Openline: strings.EqualFold(portalV2Str(f, "type"), "openline")}
	portalV2Props(f, &b)
	if b.Name == "" {
		b.Name = code
	}
	if ch.EventMode != nil {
		b.EventMode = *ch.EventMode
	}
	for _, v := range []struct {
		to *string
		v  *string
	}{{&b.WebhookURL, ch.WebhookURL}, {&b.Background, ch.Background}} {
		if v.v != nil {
			*v.to = *v.v
		}
	}
	if t := portalV2Token(c); ch.BotToken != nil || t != "" {
		b.BotToken = matrixsync.HashBotToken(t)
	}
	for _, v := range []struct{ to, v *bool }{{&b.Hidden, ch.Hidden}, {&b.Reactions, ch.Reactions}, {&b.Openline, ch.Openline}} {
		if v.v != nil {
			*v.to = *v.v
		}
	}
	if err := s.portalV2Hook(c, a, b); err != nil {
		return portalOut{}, err
	}
	if b, err = s.Matrix.RegisterBot(ctx, a.ID, c.p.UserID, b); err != nil {
		return portalOut{}, err
	}
	return s.portalV2Bot(c, b)
}

// imbot.v2.Bot.update: botId, fields — properties, isHidden, isReactionsEnabled, isSupportOpenline, backgroundId,
// eventMode, webhookUrl, botToken (a new one) → {bot, users}.
func (s *Server) portalV2BotUpdate(c portalCall) (portalOut, error) {
	a, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	f := portalMap(portalParam(c.in, "fields"))
	var props matrixsync.Bot
	portalV2Props(f, &props)
	if props != (matrixsync.Bot{}) {
		if b, err = s.Matrix.UpdateBot(ctx, a.ID, b.ID, props); err != nil {
			return portalOut{}, err
		}
	}
	ch, err := s.portalV2Change(c, f)
	if err != nil {
		return portalOut{}, err
	}
	if b, err = s.Matrix.SetBotV2(ctx, a.ID, b.ID, ch); err != nil {
		return portalOut{}, err
	}
	if err := s.portalV2Hook(c, a, b); err != nil {
		return portalOut{}, err
	}
	return s.portalV2Bot(c, b)
}

// imbot.v2.Bot.get: botId or code → {bot, users}.
func (s *Server) portalV2BotGet(c portalCall) (portalOut, error) {
	if _, ok := portalParamOK(c.in, "BOT_ID"); !ok {
		c.in["BOT_ID"] = portalParam(c.in, "code")
	}
	_, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalV2Bot(c, b)
}

// imbot.v2.Bot.list: filter.type, limit (50), offset → {bots, users, hasNextPage}: the application's bots.
func (s *Server) portalV2BotList(c portalCall) (portalOut, error) {
	a, err := s.portalBotApp(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	list, err := s.Matrix.Bots(ctx, a.ID)
	if err != nil {
		return portalOut{}, err
	}
	typ := strings.ToLower(portalV2Str(portalMap(portalParam(c.in, "filter")), "type"))
	limit, _ := strconv.Atoi(portalV2Str(c.in, "limit"))
	if limit <= 0 {
		limit = 50
	}
	offset, _ := strconv.Atoi(portalV2Str(c.in, "offset"))
	bots, users := []any{}, []any{}
	seen := 0
	for _, b := range list {
		if typ != "" && b.V2Type() != typ {
			continue
		}
		if seen++; seen <= offset {
			continue
		}
		if len(bots) == limit {
			return portalOut{Result: map[string]any{"bots": bots, "users": users, "hasNextPage": true}}, nil
		}
		bot, err := s.Matrix.BotV2(ctx, b)
		if err != nil {
			return portalOut{}, err
		}
		bots, users = append(bots, bot), append(users, matrixsync.BotV2User(b))
	}
	return portalOut{Result: map[string]any{"bots": bots, "users": users, "hasNextPage": false}}, nil
}

// imbot.v2.Bot.unregister: botId → {result: true}; the bot gets ONIMBOTV2DELETE and leaves the messenger.
func (s *Server) portalV2BotUnregister(c portalCall) (portalOut, error) {
	out, err := s.portalBotUnregister(c)
	return portalOut{Result: map[string]any{"result": out.Result}}, err
}

// imbot.v2.Event.get: botId, offset (the events below it are taken), limit (1–1000, 100) → {events, nextOffset,
// hasMore}: the events of a «fetch» bot.
func (s *Server) portalV2EventGet(c portalCall) (portalOut, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	offset, _ := strconv.ParseInt(portalV2Str(c.in, "offset"), 10, 64)
	limit, _ := strconv.Atoi(portalV2Str(c.in, "limit"))
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 1000)
	list, next, more, err := s.Matrix.BotEvents(c.r.Context(), b.ID, offset, limit)
	if err != nil {
		return portalOut{}, err
	}
	if list == nil {
		list = []matrixsync.BotEvent{}
	}
	return portalOut{Result: map[string]any{"events": list, "nextOffset": next, "hasMore": more}}, nil
}

// portalV2Msg is the message of fields: message, keyboard, attach (an object with BLOCKS or the blocks themselves).
func portalV2Msg(f map[string]any) botMsg {
	attach := portalParam(f, "attach")
	if m := portalMap(attach); m != nil {
		if blocks, ok := portalParamOK(m, "BLOCKS"); ok {
			attach = blocks
		}
	}
	return botMsg{Message: botText(portalStr(portalParam(f, "message"))), Keyboard: portalRaw(portalParam(f, "keyboard")),
		Menu: portalRaw(portalParam(f, "menu")), Attach: portalRaw(attach)}
}

// imbot.v2.Chat.Message.send: botId, dialogId, fields (message, keyboard, attach) → {id, uuidMap, failureMap}.
func (s *Server) portalV2MessageSend(c portalCall) (portalOut, error) {
	c.in = portalV2Flat(c)
	out, err := s.portalBotMessageAdd(c)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"id": out.Result, "uuidMap": []any{}, "failureMap": []any{}}}, nil
}

// portalV2Flat is the call's parameters with fields (message, keyboard, menu, attach) as MESSAGE, KEYBOARD, MENU, ATTACH.
func portalV2Flat(c portalCall) map[string]any {
	m := portalV2Msg(portalV2Fields(c))
	in := c.in
	in["MESSAGE"] = m.Message
	for k, v := range map[string][]byte{"KEYBOARD": m.Keyboard, "MENU": m.Menu, "ATTACH": m.Attach} {
		if v != nil {
			in[k] = string(v)
		} else {
			delete(in, k)
		}
	}
	return in
}

// imbot.v2.Chat.Message.update: botId, messageId — the bot's message —, fields (message, keyboard, attach) →
// {result: true}.
func (s *Server) portalV2MessageUpdate(c portalCall) (portalOut, error) {
	c.in = portalV2Flat(c)
	out, err := s.portalBotMessageUpdate(c)
	return portalOut{Result: map[string]any{"result": out.Result}}, err
}

// imbot.v2.Chat.Message.delete: botId, messageId → {result: true}.
func (s *Server) portalV2MessageDelete(c portalCall) (portalOut, error) {
	out, err := s.portalBotMessageDelete(c)
	return portalOut{Result: map[string]any{"result": out.Result}}, err
}

// imbot.v2.Chat.Message.get: botId, messageId — of a chat of the bot → {message, user}.
func (s *Server) portalV2MessageGet(c portalCall) (portalOut, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	room, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	ev, author, err := s.Matrix.BotMessage(ctx, b.ID, room, event)
	if err != nil {
		return portalOut{}, err
	}
	msg, err := s.Matrix.V2Message(ctx, room, ev, author)
	if err != nil {
		return portalOut{}, err
	}
	user, err := s.Matrix.V2Person(ctx, author)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"message": msg, "user": user}}, nil
}

// imbot.v2.Chat.InputAction.notify: botId, dialogId → {result: true}; the others see «typing…» of the bot.
func (s *Server) portalV2Typing(c portalCall) (portalOut, error) {
	out, err := s.portalBotTyping(c)
	return portalOut{Result: map[string]any{"result": out.Result}}, err
}

// portalV2Lang is title and params of a command ({lang: text}) as its texts by language; nil — neither given.
func portalV2Lang(f map[string]any) map[string]matrixsync.CommandText {
	title, okT := portalParamOK(f, "title")
	params, okP := portalParamOK(f, "params")
	if !okT && !okP {
		return nil
	}
	out := map[string]matrixsync.CommandText{}
	for lang, v := range portalMap(title) {
		t := out[strings.ToLower(lang)]
		t.Title = portalStr(v)
		out[strings.ToLower(lang)] = t
	}
	for lang, v := range portalMap(params) {
		t := out[strings.ToLower(lang)]
		t.Params = portalStr(v)
		out[strings.ToLower(lang)] = t
	}
	return out
}

// portalV2Command is a command as imbot.v2 describes it.
func portalV2Command(cmd matrixsync.Command, bot int64) map[string]any {
	title, params := map[string]string{}, map[string]string{}
	for lang, t := range cmd.Lang {
		title[lang], params[lang] = t.Title, t.Params
	}
	return map[string]any{"id": cmd.Num, "botId": bot, "command": "/" + cmd.Command, "common": cmd.Common, "hidden": cmd.Hidden,
		"extranetSupport": cmd.Extranet, "title": title, "params": params}
}

// imbot.v2.Command.register: botId, fields — command, title, params ({lang: text}), common, hidden, extranetSupport
// → {command}; it goes to the bot as ONIMBOTV2COMMANDADD.
func (s *Server) portalV2CommandRegister(c portalCall) (portalOut, error) {
	a, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	f := portalV2Fields(c)
	cmd := matrixsync.Command{Command: strings.TrimPrefix(portalV2Str(f, "command"), "/"), Lang: portalV2Lang(f)}
	for _, v := range []struct {
		to *bool
		k  string
	}{{&cmd.Common, "common"}, {&cmd.Hidden, "hidden"}, {&cmd.Extranet, "extranetSupport"}} {
		if y := portalFlagOf(f, v.k); y != nil {
			*v.to = *y
		}
	}
	if cmd, err = s.Matrix.RegisterCommand(c.r.Context(), a.ID, b.ID, cmd); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"command": portalV2Command(cmd, b.Num)}}, nil
}

// imbot.v2.Command.update: botId, commandId, fields (title, params, common, hidden, extranetSupport) → {command}.
func (s *Server) portalV2CommandUpdate(c portalCall) (portalOut, error) {
	a, cmd, err := s.portalBotCommand(c)
	if err != nil {
		return portalOut{}, err
	}
	f := portalMap(portalParam(c.in, "fields"))
	lang := portalV2Lang(f)
	if lang != nil { // a language not given keeps its texts
		for l, t := range cmd.Lang {
			if _, ok := lang[l]; !ok {
				lang[l] = t
			}
		}
	}
	ctx := c.r.Context()
	if cmd, err = s.Matrix.UpdateCommand(ctx, a.ID, cmd.BotID, cmd.ID, matrixsync.CommandChange{Common: portalFlagOf(f, "common"),
		Hidden: portalFlagOf(f, "hidden"), Extranet: portalFlagOf(f, "extranetSupport"), Lang: lang}); err != nil {
		return portalOut{}, err
	}
	b, err := s.Matrix.AppBot(ctx, a.ID, cmd.BotID)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"command": portalV2Command(cmd, b.Num)}}, nil
}

// imbot.v2.Command.unregister: botId, commandId → {result: true}.
func (s *Server) portalV2CommandUnregister(c portalCall) (portalOut, error) {
	out, err := s.portalCommandUnregister(c)
	return portalOut{Result: map[string]any{"result": out.Result}}, err
}

// imbot.v2.Command.list: botId → {commands}.
func (s *Server) portalV2CommandList(c portalCall) (portalOut, error) {
	a, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Matrix.Commands(c.r.Context(), a.ID, b.ID)
	if err != nil {
		return portalOut{}, err
	}
	out := []any{}
	for _, cmd := range list {
		out = append(out, portalV2Command(cmd, b.Num))
	}
	return portalOut{Result: map[string]any{"commands": out}}, nil
}

// imbot.v2.Command.answer: botId, commandId, messageId — the command's message —, dialogId, fields (message,
// keyboard, attach) → {result: true}; the answer goes to the command's chat (and thread).
func (s *Server) portalV2CommandAnswer(c portalCall) (portalOut, error) {
	c.in = portalV2Flat(c)
	_, err := s.portalCommandAnswer(c)
	return portalOut{Result: map[string]any{"result": err == nil}}, err
}
