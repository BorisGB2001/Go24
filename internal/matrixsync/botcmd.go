package matrixsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// Commands and keyboards of chat bots, as imbot.command.* and KEYBOARD (migration 331): an application
// registers «/command» for its bot; a message beginning with it in a chat the bot is in (any chat for a common
// command) goes to the command's handler as ONIMCOMMANDADD instead of ONIMBOTMESSAGEADD. A message of the bot may
// carry buttons under it: a link, an action of the messenger (put or send a text, copy, call, open a chat) or a
// command of the bot — pressed, it goes to the handler as ONIMCOMMANDADD too.

// BotCommand is the event of a command (apps.Events has it too).
const BotCommand = "ONIMCOMMANDADD"

var errCommand = i18n.E("err.imbot_command")

// YN is a yes or no: "Y" / "N", or true / false.
type YN bool

func (y *YN) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case bool:
		*y = YN(t)
	case string:
		*y = YN(strings.EqualFold(t, "Y") || t == "1" || strings.EqualFold(t, "true"))
	case float64:
		*y = t != 0
	}
	return nil
}

// Command is a «/command» of a bot.
type Command struct {
	ID        string                 `json:"id"`
	BotID     string                 `json:"bot_id"`
	Command   string                 `json:"command"`
	Common    bool                   `json:"common"`
	Hidden    bool                   `json:"hidden"`
	Extranet  bool                   `json:"extranet_support"`
	Lang      map[string]CommandText `json:"lang"` // by language: ru, en …
	Handler   string                 `json:"event_command_add"`
	CreatedAt time.Time              `json:"created_at"`
}

// CommandText is what the list on «/» shows for a command: what it does and what to write after it.
type CommandText struct {
	Title  string `json:"title"`
	Params string `json:"params"`
}

// Text is the command's text in the language (ru-RU — ru), else in English, else in any it has.
func (c Command) Text(lang string) CommandText {
	if t, ok := c.Lang[strings.ToLower(strings.SplitN(lang, "-", 2)[0])]; ok {
		return t
	}
	if t, ok := c.Lang["en"]; ok {
		return t
	}
	keys := make([]string, 0, len(c.Lang))
	for k := range c.Lang {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return c.Lang[keys[0]]
	}
	return CommandText{}
}

// CommandChange is what imbot.command.update changes: nil — stays as it is.
type CommandChange struct {
	Common, Hidden, Extranet *bool
	Lang                     map[string]CommandText
	Handler                  string
}

var commandRe = regexp.MustCompile(`^[\p{L}\p{N}_.-]{1,50}$`)

// normCommand is the command's name without its «/».
func normCommand(name string) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if !commandRe.MatchString(name) {
		return name, i18n.E("err.imbot_command_name")
	}
	return name, nil
}

const commandCols = `c.id::text, c.bot_id::text, c.command, c.common, c.hidden, c.extranet, c.lang, c.handler, c.created_at`

func scanCommand(r pgx.Row, extra ...any) (Command, error) {
	var c Command
	var lang []byte
	err := r.Scan(append([]any{&c.ID, &c.BotID, &c.Command, &c.Common, &c.Hidden, &c.Extranet, &lang, &c.Handler, &c.CreatedAt}, extra...)...)
	if err == nil {
		err = json.Unmarshal(lang, &c.Lang)
	}
	return c, err
}

// RegisterCommand adds a command to a bot of the application (imbot.command.register); its handler must be bound to
// ONIMCOMMANDADD already. The same command of the bot again is an error.
func (s *Service) RegisterCommand(ctx context.Context, appID, botID string, c Command) (Command, error) {
	b, err := s.AppBot(ctx, appID, botID)
	if err != nil {
		return c, err
	}
	if c.Command, err = normCommand(c.Command); err != nil {
		return c, err
	}
	if c.Lang == nil {
		c.Lang = map[string]CommandText{}
	}
	lang, _ := json.Marshal(c.Lang)
	out, err := scanCommand(s.DB.QueryRow(ctx, `INSERT INTO im_bot_commands AS c (bot_id, command, common, hidden, extranet, lang, handler)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING RETURNING `+commandCols,
		b.ID, c.Command, c.Common, c.Hidden, c.Extranet, lang, strings.TrimSpace(c.Handler)))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errCommand
	}
	return out, err
}

// BotCommand is a command of a bot of the application by its ID or name.
func (s *Service) BotCommand(ctx context.Context, appID, botID, id string) (Command, error) {
	b, err := s.AppBot(ctx, appID, botID)
	if err != nil {
		return Command{}, err
	}
	id = strings.TrimPrefix(strings.TrimSpace(id), "/")
	c, err := scanCommand(s.DB.QueryRow(ctx, `SELECT `+commandCols+` FROM im_bot_commands c WHERE c.bot_id = $1
		AND (c.id::text = $2 OR lower(c.command) = lower($2)) ORDER BY c.id::text = $2 DESC LIMIT 1`, b.ID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, i18n.E("err.not_found")
	}
	return c, err
}

// UpdateCommand changes a command (imbot.command.update).
func (s *Service) UpdateCommand(ctx context.Context, appID, botID, id string, ch CommandChange) (Command, error) {
	c, err := s.BotCommand(ctx, appID, botID, id)
	if err != nil {
		return c, err
	}
	for _, f := range []struct{ to, v *bool }{{&c.Common, ch.Common}, {&c.Hidden, ch.Hidden}, {&c.Extranet, ch.Extranet}} {
		if f.v != nil {
			*f.to = *f.v
		}
	}
	if ch.Lang != nil {
		c.Lang = ch.Lang
	}
	if h := strings.TrimSpace(ch.Handler); h != "" {
		c.Handler = h
	}
	lang, _ := json.Marshal(c.Lang)
	_, err = s.DB.Exec(ctx, `UPDATE im_bot_commands SET common = $2, hidden = $3, extranet = $4, lang = $5, handler = $6 WHERE id::text = $1`,
		c.ID, c.Common, c.Hidden, c.Extranet, lang, c.Handler)
	return c, err
}

// DeleteCommand removes a command (imbot.command.unregister).
func (s *Service) DeleteCommand(ctx context.Context, appID, botID, id string) error {
	c, err := s.BotCommand(ctx, appID, botID, id)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM im_bot_commands WHERE id::text = $1`, c.ID)
	return err
}

// Commands are the commands of a bot of the application.
func (s *Service) Commands(ctx context.Context, appID, botID string) ([]Command, error) {
	b, err := s.AppBot(ctx, appID, botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+commandCols+` FROM im_bot_commands c WHERE c.bot_id = $1 ORDER BY lower(c.command)`, b.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Command, error) { return scanCommand(r) })
}

// ChatCommand is a command in the list the messenger shows on «/».
type ChatCommand struct {
	Command string `json:"command"`
	Title   string `json:"title"`
	Params  string `json:"params"`
	Bot     string `json:"bot"`
}

// chatCommand is a command one may use in a chat, with its bot.
type chatCommand struct {
	Command
	bot Bot
}

// chatCommands are the commands of the bots of the chat and the common ones that the employee (a guest — those
// for guests) may use there; name "" — all of them, else that one command.
func (s *Service) chatCommands(ctx context.Context, userID string, mem map[string]string, name string) ([]chatCommand, error) {
	in := make([]string, 0, len(mem))
	for mx := range mem {
		in = append(in, mx)
	}
	rows, err := s.DB.Query(ctx, `SELECT `+commandCols+`, `+botCols+` FROM im_bot_commands c JOIN im_bots b ON b.id = c.bot_id
		JOIN apps a ON a.id = b.app_id AND a.installed_at IS NOT NULL
		WHERE (c.common OR b.mxid = ANY($1)) AND (c.extranet OR NOT `+tasks.GuestSQL("$2")+`) AND ($3 = '' OR lower(c.command) = lower($3))
		ORDER BY lower(c.command), b.name`, in, userID, name)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (chatCommand, error) {
		var cc chatCommand
		b := &cc.bot
		var err error
		cc.Command, err = scanCommand(r, &b.ID, &b.AppID, &b.Code, &b.Type, &b.Name, &b.LastName, &b.WorkPosition, &b.Color,
			&b.MessageHandler, &b.WelcomeHandler, &b.DeleteHandler, &b.UserID, &b.CreatedAt, &b.mxid, &b.since)
		return cc, err
	})
}

// ChatCommands are the commands the employee is offered on «/» in the chat (not the hidden ones), in the language.
func (s *Service) ChatCommands(ctx context.Context, userID, roomID, lang string) ([]ChatCommand, error) {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, err
	}
	mem, err := s.M.Members(ctx, roomID, tok)
	if err != nil {
		return nil, forbidden(err)
	}
	list, err := s.chatCommands(ctx, userID, mem, "")
	if err != nil {
		return nil, err
	}
	out := []ChatCommand{}
	for _, c := range list {
		if !c.Hidden {
			t := c.Text(lang)
			out = append(out, ChatCommand{Command: c.Command.Command, Title: t.Title, Params: t.Params, Bot: c.bot.FullName()})
		}
	}
	return out, nil
}

// commandOf: the text is «/command params».
func commandOf(body string) (name, params string, ok bool) {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "/") {
		return "", "", false
	}
	name, params, _ = strings.Cut(body[1:], " ")
	if n, p, cut := strings.Cut(name, "\n"); cut { // «/command» and the params on the next line
		name, params = n, p+" "+params
	}
	if !commandRe.MatchString(name) {
		return "", "", false
	}
	return name, strings.TrimSpace(params), true
}

// botCommands: one's own new message «/command …» goes to the handlers of the commands of that name in the chat.
func (s *Service) botCommands(ctx context.Context, me, tok, roomID, root string, m Message) error {
	name, params, ok := commandOf(m.Body)
	if !ok {
		return nil
	}
	who, err := s.people(ctx, []string{me})
	if err != nil {
		return err
	}
	from := who[me]
	if from.ID == "" || from.Bot {
		return nil
	}
	mem, err := s.M.Members(ctx, roomID, tok)
	if err != nil {
		return err
	}
	list, err := s.chatCommands(ctx, from.ID, mem, name)
	if err != nil {
		return err
	}
	for _, c := range list {
		if err := s.queueCommand(ctx, c, roomID, mem, from, m.ID, m.Body, params, "TEXTAREA", root); err != nil {
			return err
		}
	}
	return nil
}

// queueCommand queues ONIMCOMMANDADD of the command to its handler: the chat as the bot sees it, the message (or the
// bot's message with the pressed button) and the command's params.
func (s *Service) queueCommand(ctx context.Context, c chatCommand, roomID string, mem map[string]string, from imPerson,
	eventID, body, params, how, root string) error {
	_, in := mem[c.bot.mxid]
	p := botParams(c.bot, roomID, in && len(mem) == 2, from, eventID, body)
	if root != "" {
		p["THREAD_ID"] = root
	}
	return s.botQueue(ctx, c.bot, BotCommand, c.Handler, map[string]any{"PARAMS": p, "USER": botUser(from),
		"COMMAND": map[string]any{c.ID: map[string]any{"BOT_ID": c.bot.ID, "BOT_CODE": c.bot.Code, "COMMAND": c.Command.Command,
			"COMMAND_ID": c.ID, "COMMAND_PARAMS": params, "COMMAND_CONTEXT": how, "MESSAGE_ID": eventID}}})
}

// botParams is PARAMS of a bot's event about a message: in the bot's private chat DIALOG_ID is the person's ID,
// in a group chat it is the chat's.
func botParams(b Bot, room string, private bool, from imPerson, eventID, body string) map[string]any {
	p := map[string]any{"DIALOG_ID": room, "CHAT_TYPE": "C", "MESSAGE_TYPE": "C", "CHAT_ID": room, "TO_CHAT_ID": room,
		"MESSAGE_ID": eventID, "MESSAGE": body, "FROM_USER_ID": from.ID}
	if private {
		p["DIALOG_ID"], p["CHAT_TYPE"], p["MESSAGE_TYPE"], p["TO_USER_ID"] = from.ID, "P", "P", b.ID
		delete(p, "TO_CHAT_ID")
	}
	return p
}

// botHasCommand: the bot has the command (its message goes to the command, not to ONIMBOTMESSAGEADD).
func (s *Service) botHasCommand(ctx context.Context, botID, body string) (bool, error) {
	name, _, ok := commandOf(body)
	if !ok {
		return false, nil
	}
	var has bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM im_bot_commands WHERE bot_id::text = $1 AND lower(command) = lower($2))`,
		botID, name).Scan(&has)
	return has, err
}

// PressButton: the employee pressed a button of a bot's message that runs the bot's command (the button must be
// there and not disabled); the command's handler gets it with COMMAND_CONTEXT KEYBOARD.
func (s *Service) PressButton(ctx context.Context, userID, roomID, eventID, command, params string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	ev, err := s.M.EventAs(ctx, tok, roomID, eventID)
	if err != nil {
		return forbidden(err)
	}
	who, err := s.people(ctx, []string{me, ev.Sender})
	if err != nil {
		return err
	}
	bot, from := who[ev.Sender], who[me]
	if !bot.Bot || from.ID == "" {
		return i18n.E("err.forbidden")
	}
	if !slices.ContainsFunc(keyboardOf(lastContent(ev)), func(b Button) bool {
		return !bool(b.Disabled) && strings.EqualFold(b.Command, command) && b.CommandParams == params
	}) {
		return i18n.E("err.not_found")
	}
	mem, err := s.M.Members(ctx, roomID, tok)
	if err != nil {
		return err
	}
	list, err := s.chatCommands(ctx, from.ID, map[string]string{ev.Sender: "join"}, command)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.bot.ID == bot.ID {
			return s.queueCommand(ctx, c, roomID, mem, from, eventID, strings.TrimSpace("/"+command+" "+params), params, "KEYBOARD", "")
		}
	}
	return i18n.E("err.not_found")
}

// CommandRoom is where the bot answers a command (imbot.command.answer): the chat of the command's message, or the
// bot's private chat with its author when the bot is not in that chat (a common command); thread — the command's
// thread.
func (s *Service) CommandRoom(ctx context.Context, appID string, c Command, messageID string) (room, thread string, err error) {
	var from string
	err = s.DB.QueryRow(ctx, `SELECT coalesce(d.data->'PARAMS'->>'CHAT_ID', ''), coalesce(d.data->'PARAMS'->>'FROM_USER_ID', ''),
		coalesce(d.data->'PARAMS'->>'THREAD_ID', '') FROM app_event_deliveries d JOIN app_events e ON e.id = d.event_id
		WHERE e.app_id::text = $1 AND d.event = $2 AND d.data->'PARAMS'->>'MESSAGE_ID' = $3 AND d.data->'COMMAND' ? $4
		ORDER BY d.id DESC LIMIT 1`, appID, BotCommand, messageID, c.ID).Scan(&room, &from, &thread)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", i18n.E("err.not_found")
	}
	if err != nil {
		return "", "", err
	}
	_, tok, err := s.botPuppet(ctx, c.BotID)
	if err != nil {
		return "", "", err
	}
	if _, err := s.M.Members(ctx, room, tok); err != nil {
		room, err = s.DirectChat(ctx, c.BotID, from)
		return room, "", err
	}
	return room, thread, nil
}

// ---------- keyboards ----------

// Button is a button of a bot's message (KEYBOARD); TYPE NEWLINE starts a new line of buttons.
type Button struct {
	Type          string `json:"type,omitempty"`
	Text          string `json:"text,omitempty"`
	Link          string `json:"link,omitempty"`
	Command       string `json:"command,omitempty"`
	CommandParams string `json:"command_params,omitempty"`
	BgColor       string `json:"bg_color,omitempty"`
	TextColor     string `json:"text_color,omitempty"`
	Display       string `json:"display,omitempty"` // LINE — as wide as its text; BLOCK — the whole line
	Block         YN     `json:"block,omitempty"`   // one press blocks the buttons of the message
	Disabled      YN     `json:"disabled,omitempty"`
	Action        string `json:"action,omitempty"` // PUT, SEND, COPY, CALL, DIALOG
	ActionValue   string `json:"action_value,omitempty"`
}

var colorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{3,8}$`)

// NormKeyboard checks the buttons the application gives.
func NormKeyboard(kb []Button) ([]Button, error) {
	if len(kb) > 100 {
		return nil, i18n.E("err.imbot_keyboard")
	}
	out := make([]Button, 0, len(kb))
	for _, b := range kb {
		if strings.EqualFold(b.Type, "NEWLINE") {
			out = append(out, Button{Type: "NEWLINE"})
			continue
		}
		b.Type = ""
		b.Text, b.Link, b.Command = strings.TrimSpace(b.Text), strings.TrimSpace(b.Link), strings.TrimPrefix(strings.TrimSpace(b.Command), "/")
		b.Display, b.Action = strings.ToUpper(strings.TrimSpace(b.Display)), strings.ToUpper(strings.TrimSpace(b.Action))
		if b.Text == "" || len([]rune(b.Text)) > 100 {
			return nil, i18n.E("err.imbot_keyboard")
		}
		if b.Link != "" {
			if u, err := url.Parse(b.Link); err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
				return nil, i18n.E("err.imbot_keyboard")
			}
		}
		if b.Command != "" && !commandRe.MatchString(b.Command) {
			return nil, i18n.E("err.imbot_command_name")
		}
		if !slices.Contains([]string{"", "PUT", "SEND", "COPY", "CALL", "DIALOG"}, b.Action) {
			return nil, i18n.E("err.imbot_keyboard")
		}
		if b.Display != "BLOCK" {
			b.Display = "LINE"
		}
		if !colorRe.MatchString(b.BgColor) {
			b.BgColor = ""
		}
		if !colorRe.MatchString(b.TextColor) {
			b.TextColor = ""
		}
		out = append(out, b)
	}
	return out, nil
}

// keyboardOf is the keyboard a message's content carries.
func keyboardOf(content map[string]any) []Button {
	raw, ok := content["go24.keyboard"]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var kb []Button
	if json.Unmarshal(b, &kb) != nil {
		return nil
	}
	return kb
}

// lastContent is the message's content as its latest edit has it.
func lastContent(ev matrix.Event) map[string]any {
	if ed := ev.Unsigned.Relations.Replace; ed != nil {
		if nc, ok := ed.Content["m.new_content"].(map[string]any); ok {
			return nc
		}
	}
	return ev.Content
}

// keyboardRows are the lines of buttons as the message shows them: NEWLINE and a button of DISPLAY BLOCK break them.
func keyboardRows(kb []Button) [][]Button {
	var rows [][]Button
	var row []Button
	flush := func() {
		if len(row) > 0 {
			rows = append(rows, row)
			row = nil
		}
	}
	for _, b := range kb {
		switch {
		case b.Type == "NEWLINE":
			flush()
		case b.Display == "BLOCK":
			flush()
			rows = append(rows, []Button{b})
		default:
			row = append(row, b)
		}
	}
	flush()
	return rows
}

// BotSend is a message of the bot (imbot.message.add, imbot.command.answer) with its buttons.
func (s *Service) BotSend(ctx context.Context, botID, roomID, body, root string, kb []Button) (Message, error) {
	me, tok, err := s.token(ctx, botID)
	if err != nil {
		return Message{}, err
	}
	if body = strings.TrimSpace(body); body == "" {
		return Message{}, i18n.E("err.comment_empty")
	}
	content := textContent(body)
	if err := s.mention(ctx, content, body, ""); err != nil {
		return Message{}, err
	}
	if len(kb) > 0 {
		content["go24.keyboard"] = kb
	}
	m, err := s.post(ctx, me, tok, roomID, root, content)
	m.Keyboard, m.SenderBot = keyboardRows(kb), true
	return m, err
}
