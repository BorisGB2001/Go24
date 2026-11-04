package matrixsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/matrix"
)

// Chat bots 2.0, imbot.v2.* (GO24DEV-954): the same bot — its own Matrix account, read by the worker — but
// its events are not posted to the handlers of imbot.register. A bot of eventMode «fetch» keeps them in im_bot_events
// (24 hours) and its application takes them by imbot.v2.Event.get; of «webhook» — they are posted to its webhookUrl,
// posts events. The data is that of imbot.v2: objects bot, message, chat, user in camelCase with numbers.

// The events of a bot of imbot.v2 (apps.Events has them too).
const (
	BotV2MessageAdd    = "ONIMBOTV2MESSAGEADD"
	BotV2MessageUpdate = "ONIMBOTV2MESSAGEUPDATE"
	BotV2MessageDelete = "ONIMBOTV2MESSAGEDELETE"
	BotV2JoinChat      = "ONIMBOTV2JOINCHAT"
	BotV2Delete        = "ONIMBOTV2DELETE"
	BotV2CommandAdd    = "ONIMBOTV2COMMANDADD"
	BotV2Reaction      = "ONIMBOTV2REACTIONCHANGE"
	BotV2ContextGet    = "ONIMBOTV2CONTEXTGET"
)

// BotV2Events are the events a bot of imbot.v2 gets, for the subscription of its webhookUrl.
var BotV2Events = []string{BotV2MessageAdd, BotV2MessageUpdate, BotV2MessageDelete, BotV2JoinChat, BotV2Delete, BotV2CommandAdd, BotV2Reaction,
	BotV2ContextGet}

// v2Of is the event of imbot.v2 for one of imbot.
var v2Of = map[string]string{BotMessageAdd: BotV2MessageAdd, BotJoinChat: BotV2JoinChat, BotDelete: BotV2Delete, BotCommand: BotV2CommandAdd}

// botEventsKept is how long the events of a «fetch» bot wait to be taken.
const botEventsKept = 24 * time.Hour

// V2 tells a bot of imbot.v2.
func (b Bot) V2() bool { return b.EventMode != "" }

// The types of imbot.v2 and those go24 keeps (of imbot): bot — B, supervisor — S, personal — H; openline is B with
// open lines on.
var botV2Types = map[string]string{"bot": "B", "supervisor": "S", "personal": "H", "openline": "B"}

// V2Type is the type of the bot as imbot.v2 names it.
func (b Bot) V2Type() string {
	switch {
	case b.Type == "S":
		return "supervisor"
	case b.Type == "H":
		return "personal"
	case b.Openline:
		return "openline"
	}
	return "bot"
}

// BotV2Type is go24's type of a type of imbot.v2 ("" — bot); ok false for an unknown one.
func BotV2Type(t string) (string, bool) {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		t = "bot"
	}
	v, ok := botV2Types[t]
	return v, ok
}

// HashBotToken is how botToken is kept.
func HashBotToken(t string) string {
	if t = strings.TrimSpace(t); t == "" {
		return ""
	}
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// BotV2Change is what imbot.v2.Bot.update changes; nil — kept.
type BotV2Change struct {
	EventMode, WebhookURL, BotToken, Background *string
	Hidden, Reactions, Openline                 *bool
}

// SetBotV2 changes the settings of imbot.v2 of the application's bot: its events, botToken, hidden, reactions, open
// lines, background. A bot of imbot becomes one of imbot.v2 when it gets an eventMode.
func (s *Service) SetBotV2(ctx context.Context, appID, id string, ch BotV2Change) (Bot, error) {
	b, err := s.AppBot(ctx, appID, id)
	if err != nil {
		return b, err
	}
	for _, f := range []struct{ to, v *string }{{&b.EventMode, ch.EventMode}, {&b.WebhookURL, ch.WebhookURL}, {&b.Background, ch.Background}} {
		if f.v != nil {
			*f.to = strings.TrimSpace(*f.v)
		}
	}
	if ch.BotToken != nil {
		b.BotToken = HashBotToken(*ch.BotToken)
	}
	for _, f := range []struct{ to, v *bool }{{&b.Hidden, ch.Hidden}, {&b.Reactions, ch.Reactions}, {&b.Openline, ch.Openline}} {
		if f.v != nil {
			*f.to = *f.v
		}
	}
	if b.EventMode != "" && b.EventMode != "fetch" && b.EventMode != "webhook" {
		return b, i18n.E("err.imbot_event_mode", b.EventMode)
	}
	_, err = s.DB.Exec(ctx, `UPDATE im_bots SET event_mode = $2, webhook_url = $3, bot_token = $4, hidden = $5, reactions = $6,
		openline = $7, background = $8 WHERE id::text = $1`, b.ID, b.EventMode, b.WebhookURL, b.BotToken, b.Hidden, b.Reactions, b.Openline, b.Background)
	return b, err
}

// BotEvent is an event of a «fetch» bot, as imbot.v2.Event.get gives it.
type BotEvent struct {
	ID   int64           `json:"eventId"`
	Type string          `json:"type"`
	Date string          `json:"date"`
	Data json.RawMessage `json:"data"`
}

// BotEvents are the events of a «fetch» bot from offset (those below it are taken: confirmed and gone), at most limit;
// next is the offset of the next call, more — there are more already.
func (s *Service) BotEvents(ctx context.Context, botID string, offset int64, limit int) (list []BotEvent, next int64, more bool, err error) {
	return s.eventPage(ctx, "im_bot_events", "bot_id", botID, offset, limit)
}

// eventPage is a page of a log of events of imbot.v2 or im.v2 (table, its owner's column col): what is below offset
// or older than a day goes, then from offset at most limit.
func (s *Service) eventPage(ctx context.Context, table, col, owner string, offset int64, limit int) (list []BotEvent, next int64, more bool, err error) {
	if _, err = s.DB.Exec(ctx, `DELETE FROM `+table+` WHERE `+col+`::text = $1 AND (id < $2 OR created_at < $3)`,
		owner, offset, time.Now().Add(-botEventsKept)); err != nil {
		return nil, 0, false, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id, type, created_at, data::text FROM `+table+` WHERE `+col+`::text = $1 AND id >= $2
		ORDER BY id LIMIT $3`, owner, offset, limit+1)
	if err != nil {
		return nil, 0, false, err
	}
	list, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (BotEvent, error) {
		var e BotEvent
		var at time.Time
		var data string
		err := r.Scan(&e.ID, &e.Type, &at, &data)
		e.Date, e.Data = at.Format(time.RFC3339), json.RawMessage(data)
		return e, err
	})
	if err != nil {
		return nil, 0, false, err
	}
	if more = len(list) > limit; more {
		list = list[:limit]
	}
	next = offset
	if len(list) > 0 {
		next = list[len(list)-1].ID + 1
	}
	return list, next, more, nil
}

// queueV2 keeps an event of a bot of imbot.v2 for its application (data as imbot gives it, made into imbot.v2's): a
// «fetch» bot — until Event.get takes it, a «webhook» bot — to post it to webhookUrl.
func (s *Service) queueV2(ctx context.Context, b Bot, event string, data map[string]any) error {
	name, ok := v2Of[event]
	if !ok || b.AppID == "" {
		return nil
	}
	out, err := s.v2Data(ctx, b, name, data)
	if err != nil {
		return err
	}
	return s.QueueV2(ctx, b, name, out)
}

// QueueV2 keeps an event of imbot.v2 (its data made already) of the bot for its application.
func (s *Service) QueueV2(ctx context.Context, b Bot, event string, data map[string]any) error {
	if b.EventMode == "webhook" {
		data["bot"] = map[string]any{"id": b.Num, "code": b.Code} // apps adds its tokens: «auth»
	} else {
		bot, err := s.BotV2(ctx, b)
		if err != nil {
			return err
		}
		data["bot"] = bot
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if b.EventMode != "webhook" {
		_, err = s.DB.Exec(ctx, `INSERT INTO im_bot_events (bot_id, type, data) VALUES ($1, $2, $3)`, b.ID, event, raw)
		return err
	}
	tag, err := s.DB.Exec(ctx, `INSERT INTO app_event_deliveries (event_id, event, data) SELECT e.id, $3, $4 FROM app_events e
		JOIN apps a ON a.id = e.app_id WHERE e.app_id::text = $1 AND e.event = $3 AND e.handler_url = $2 AND a.installed_at IS NOT NULL`,
		b.AppID, b.WebhookURL, event, raw)
	if err == nil && tag.RowsAffected() > 0 && s.BotQueued != nil {
		s.BotQueued()
	}
	return err
}

// BotV2 is the bot as imbot.v2 describes it to its owner (Bot.get, the events of «fetch»).
func (s *Service) BotV2(ctx context.Context, b Bot) (map[string]any, error) {
	var cmds int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM im_bot_commands WHERE bot_id::text = $1`, b.ID).Scan(&cmds); err != nil {
		return nil, err
	}
	var bg any
	if b.Background != "" {
		bg = b.Background
	}
	mode := b.EventMode
	if mode == "" {
		mode = "fetch"
	}
	return map[string]any{"id": b.Num, "code": b.Code, "type": b.V2Type(), "isHidden": b.Hidden, "isSupportOpenline": b.Openline,
		"isReactionsEnabled": b.Reactions, "backgroundId": bg, "language": botLang(), "moduleId": "rest", "eventMode": mode,
		"countMessage": 0, "countCommand": cmds, "countChat": 0, "countUser": 0}, nil
}

// BotV2User is the bot's account as a user of imbot.v2.
func BotV2User(b Bot) map[string]any {
	return map[string]any{"id": b.Num, "active": b.AppID != "" || b.Builtin != "", "name": b.FullName(), "firstName": b.Name,
		"lastName": b.LastName, "workPosition": b.WorkPosition, "color": b.Color, "avatar": "", "gender": "M", "birthday": "",
		"extranet": false, "bot": true, "connector": false, "externalAuthId": "bot", "status": "online", "idle": false,
		"lastActivityDate": false, "absent": false, "departments": []int64{}, "phones": false, "type": "bot"}
}

// botLang is the language of the installation gives it: ru, en …
func botLang() string { return strings.SplitN(i18n.Default, "-", 2)[0] }

// V2Person is an employee (or a bot) by go24 ID as a user of imbot.v2; nil when there is none.
func (s *Service) V2Person(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, nil
	}
	var num int64
	var active, guest bool
	var name, first, last, position, gender string
	var seen *time.Time
	err := s.DB.QueryRow(ctx, `SELECT u.num, u.active, u.guest, u.name, u.first_name, u.last_name, u.position, u.gender, u.seen_at
		FROM users u WHERE u.id::text = $1`, id).Scan(&num, &active, &guest, &name, &first, &last, &position, &gender, &seen)
	if errors.Is(err, pgx.ErrNoRows) {
		b, err := scanBot(s.DB.QueryRow(ctx, `SELECT `+botCols+` FROM im_bots b WHERE b.id::text = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return BotV2User(b), nil
	}
	if err != nil {
		return nil, err
	}
	typ := "employee"
	if guest {
		typ = "extranet"
	}
	var last2 any = false
	if seen != nil {
		last2 = seen.Format(time.RFC3339)
	}
	return map[string]any{"id": num, "active": active, "name": name, "firstName": first, "lastName": last, "workPosition": position,
		"color": "", "avatar": "", "gender": gender, "birthday": "", "extranet": guest, "bot": false, "connector": false,
		"externalAuthId": "default", "status": "online", "idle": false, "lastActivityDate": last2, "absent": false,
		"departments": []int64{}, "phones": false, "type": typ}, nil
}

// V2Chat is a chat of the bot as imbot.v2 describes it in events: its number, dialogId (in the bot's private chat —
// the person's number), type, name, owner (the number of who made it), color and textFieldEnabled (Y, N).
func (s *Service) V2Chat(ctx context.Context, b Bot, room string) (map[string]any, error) {
	_, tok, err := s.botToken(ctx, b.ID)
	if err != nil {
		tok = "" // a bot that is gone: the chat by its number only
	}
	return s.v2Chat(ctx, b.mxid, tok, room)
}

// v2Chat is a chat as the account me (its token tok) sees it, in the form of imbot.v2 and im.v2: in a private chat
// dialogId is the number of the other one.
func (s *Service) v2Chat(ctx context.Context, me, tok, room string) (map[string]any, error) {
	num, err := s.PortalChatNum(ctx, room)
	if err != nil {
		return nil, err
	}
	var color string
	var field bool
	if err := s.DB.QueryRow(ctx, `SELECT color, text_field FROM portal_chats WHERE num = $1`, num).Scan(&color, &field); err != nil {
		return nil, err
	}
	out := map[string]any{"id": num, "dialogId": "chat" + strconv.FormatInt(num, 10), "type": "chat", "name": "", "description": "", "entityType": "",
		"owner": 0, "avatar": "", "color": color, "textFieldEnabled": map[bool]string{true: "Y", false: "N"}[field]}
	if tok == "" {
		return out, nil
	}
	var name struct {
		Name  string `json:"name"`
		Topic string `json:"topic"`
	}
	_ = s.M.StateAs(ctx, tok, room, "m.room.name", "", &name)
	_ = s.M.StateAs(ctx, tok, room, "m.room.topic", "", &name)
	out["name"], out["description"] = name.Name, name.Topic
	var create struct {
		Creator string `json:"creator"`
	}
	_ = s.M.StateAs(ctx, tok, room, "m.room.create", "", &create)
	if mem, err := s.M.Members(ctx, room, tok); err == nil {
		if private, peer := s.botPeer(ctx, me, mem); private && peer.ID != "" {
			n, err := s.portalPerson(ctx, peer.ID)
			if err != nil {
				return nil, err
			}
			out["type"], out["dialogId"] = "private", strconv.FormatInt(toInt64(n), 10)
		}
	}
	if create.Creator != "" {
		if who, err := s.people(ctx, []string{create.Creator}); err == nil && who[create.Creator].ID != "" {
			n, err := s.portalPerson(ctx, who[create.Creator].ID)
			if err != nil {
				return nil, err
			}
			out["owner"] = n
		}
	}
	return out, nil
}

// toInt64 is a number of portalPerson (an ID left as it is — 0).
func toInt64(v any) int64 {
	n, _ := v.(int64)
	return n
}

// V2Message is a message of the chat as imbot.v2 describes it: its number, the chat's, the author's, date and text.
func (s *Service) V2Message(ctx context.Context, room string, ev matrix.Event, authorID string) (map[string]any, error) {
	num, err := s.PortalMessageNum(ctx, room, ev.EventID)
	if err != nil {
		return nil, err
	}
	chat, err := s.PortalChatNum(ctx, room)
	if err != nil {
		return nil, err
	}
	author, err := s.portalPerson(ctx, authorID)
	if err != nil {
		return nil, err
	}
	body, _ := ev.Content["body"].(string)
	if nc, ok := ev.Content["m.new_content"].(map[string]any); ok {
		body, _ = nc["body"].(string)
	}
	at := time.Now()
	if ev.TS > 0 {
		at = time.UnixMilli(ev.TS)
	}
	params := map[string]any{}
	if f := fileOf(ev.Content); f != nil { // fileId of imbot.v2.File.download is the number of the file's message
		params["FILE_NAME"], params["FILE_ID"] = f.Name, []int64{num}
	}
	mt, _ := ev.Content["msgtype"].(string)
	return map[string]any{"id": num, "chatId": chat, "authorId": author, "date": at.Format(time.RFC3339), "text": stripReplyFallback(body),
		"isSystem": mt == "m.notice", "uuid": "", "forward": nil, "params": params, "viewedByOthers": false}, nil
}

// v2Data makes the data of an event of imbot (PARAMS, USER, COMMAND with go24's IDs) into that of imbot.v2.
func (s *Service) v2Data(ctx context.Context, b Bot, event string, data map[string]any) (map[string]any, error) {
	out := map[string]any{"language": botLang()}
	if event == BotV2Delete {
		return out, nil
	}
	p, _ := data["PARAMS"].(map[string]any)
	room, _ := p["CHAT_ID"].(string)
	if room == "" {
		return out, nil
	}
	chat, err := s.V2Chat(ctx, b, room)
	if err != nil {
		return nil, err
	}
	out["chat"] = chat
	if event == BotV2JoinChat {
		out["dialogId"] = chat["dialogId"]
	}
	from, _ := p["FROM_USER_ID"].(string)
	if from == "" {
		from, _ = p["USER_ID"].(string)
	}
	user, err := s.V2Person(ctx, from)
	if err != nil {
		return nil, err
	}
	out["user"] = user
	if id, _ := p["MESSAGE_ID"].(string); id != "" {
		body, _ := p["MESSAGE"].(string)
		ev := matrix.Event{EventID: id, Content: map[string]any{"body": body}}
		if fs, ok := p["FILES"].([]map[string]any); ok && len(fs) > 0 {
			ev.Content["msgtype"], ev.Content["body"] = "m.file", fs[0]["name"]
		}
		m, err := s.V2Message(ctx, room, ev, from)
		if err != nil {
			return nil, err
		}
		if fs, ok := p["FILES"].([]map[string]any); ok && len(fs) > 0 { // what imbot.v2.File.download takes
			m["params"] = map[string]any{"FILE_NAME": fs[0]["name"], "FILE_ID": []int64{toInt64(m["id"])}}
		}
		m["text"] = body
		out["message"] = m
	}
	for id, v := range mapOf(data["COMMAND"]) {
		c, _ := v.(map[string]any)
		var num int64
		if err := s.DB.QueryRow(ctx, `SELECT num FROM im_bot_commands WHERE id::text = $1`, id).Scan(&num); err != nil {
			return nil, err
		}
		name, _ := c["COMMAND"].(string)
		how, _ := c["COMMAND_CONTEXT"].(string)
		out["command"] = map[string]any{"id": num, "command": "/" + name, "params": c["COMMAND_PARAMS"], "context": strings.ToLower(how)}
	}
	return out, nil
}

// mapOf is v as a map (nil — none).
func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// botV2Change turns an edit or a message taken back in a chat of a bot of imbot.v2 into ONIMBOTV2MESSAGEUPDATE or
// ONIMBOTV2MESSAGEDELETE — in its private chats, for a supervisor in every chat, an edit also when it mentions the bot;
// a reaction on the bot's message, put or taken back, into ONIMBOTV2REACTIONCHANGE.
func (s *Service) botV2Change(ctx context.Context, b Bot, tok, room string, ev matrix.Event, from imPerson) error {
	if done, err := s.botV2Reaction(ctx, b, tok, room, ev, from); done || err != nil {
		return err
	}
	mem, err := s.M.Members(ctx, room, tok)
	if err != nil {
		return err
	}
	private, _ := s.botPeer(ctx, b.mxid, mem)
	if ev.Type == "m.room.redaction" {
		if !private && b.Type != "S" {
			return nil
		}
		target := ev.Redacts
		if target == "" {
			target, _ = ev.Content["redacts"].(string)
		}
		var num int64 // only a message the bot heard of has a number
		if err := s.DB.QueryRow(ctx, `SELECT num FROM portal_messages WHERE event_id = $1`, target).Scan(&num); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		chat, err := s.V2Chat(ctx, b, room)
		if err != nil {
			return err
		}
		user, err := s.V2Person(ctx, from.ID)
		if err != nil {
			return err
		}
		return s.QueueV2(ctx, b, BotV2MessageDelete, map[string]any{"messageId": num, "chat": chat, "user": user, "language": botLang()})
	}
	rel, _ := ev.Content["m.relates_to"].(map[string]any)
	target, _ := rel["event_id"].(string)
	nc, _ := ev.Content["m.new_content"].(map[string]any)
	if target == "" || nc == nil || !private && b.Type != "S" && !mentions(nc, b.mxid) {
		return nil
	}
	chat, err := s.V2Chat(ctx, b, room)
	if err != nil {
		return err
	}
	user, err := s.V2Person(ctx, from.ID)
	if err != nil {
		return err
	}
	m, err := s.V2Message(ctx, room, matrix.Event{EventID: target, Content: ev.Content, TS: ev.TS}, from.ID)
	if err != nil {
		return err
	}
	return s.QueueV2(ctx, b, BotV2MessageUpdate, map[string]any{"message": m, "chat": chat, "user": user, "language": botLang()})
}

// BotMessage is a message of a chat of the bot as the bot reads it (imbot.v2.Chat.Message.get), with its author's
// go24 ID ("" — someone not in go24).
func (s *Service) BotMessage(ctx context.Context, botID, room, event string) (matrix.Event, string, error) {
	_, tok, err := s.botToken(ctx, botID)
	if err != nil {
		return matrix.Event{}, "", err
	}
	ev, err := s.M.EventAs(ctx, tok, room, event)
	if err != nil {
		return ev, "", forbidden(err)
	}
	if r := ev.Unsigned.Relations.Replace; r != nil {
		if nc, ok := r.Content["m.new_content"].(map[string]any); ok {
			ev.Content = nc
		}
	}
	who, err := s.people(ctx, []string{ev.Sender})
	if err != nil {
		return ev, "", err
	}
	return ev, who[ev.Sender].ID, nil
}

// BotReactions are the reactions of imbot.v2 by their codes (imbot.v2.Chat.Message.Reaction.add).
var BotReactions = map[string]string{
	"like": "👍", "dislike": "👎", "faceWithTearsOfJoy": "😂", "redHeart": "❤️", "neutralFace": "😐", "fire": "🔥", "cry": "😢",
	"slightlySmilingFace": "🙂", "winkingFace": "😉", "laugh": "😆", "kiss": "😘", "wonder": "😲", "slightlyFrowningFace": "🙁",
	"loudlyCryingFace": "😭", "faceWithStuckOutTongue": "😛", "faceWithStuckOutTongueAndWinkingEye": "😜",
	"smilingFaceWithSunglasses": "😎", "confusedFace": "😕", "flushedFace": "😳", "thinkingFace": "🤔", "angry": "😠",
	"smilingFaceWithHorns": "😈", "faceWithThermometer": "🤒", "facepalm": "🤦", "poo": "💩", "flexedBiceps": "💪",
	"clappingHands": "👏", "raisedHand": "✋", "smilingFaceWithHeartEyes": "😍", "smilingFaceWithHearts": "🥰", "pleadingFace": "🥺",
	"relievedFace": "😌", "foldedHands": "🙏", "okHand": "👌", "signHorns": "🤘", "loveYouGesture": "🤟", "clownFace": "🤡",
	"partyingFace": "🥳", "questionMark": "❓", "exclamationMark": "❗", "lightBulb": "💡", "bomb": "💣", "sleepingSymbol": "💤",
	"crossMark": "❌", "whiteHeavyCheckMark": "✅", "eyes": "👀", "handshake": "🤝", "hundredPoints": "💯",
}

// ReactionEmoji is the emoji of a reaction's code (any case); ok false — no such code.
func ReactionEmoji(code string) (string, bool) {
	for k, e := range BotReactions {
		if strings.EqualFold(k, strings.TrimSpace(code)) {
			return e, true
		}
	}
	return "", false
}

// reactionCode is the code of imbot.v2 of an emoji; one it has no code for — the emoji itself.
func reactionCode(emoji string) string {
	bare := strings.ReplaceAll(emoji, "\uFE0F", "")
	for k, e := range BotReactions {
		if strings.ReplaceAll(e, "\uFE0F", "") == bare {
			return k
		}
	}
	return emoji
}

// botV2Reaction turns a reaction on a message of the bot (when it takes reactions) into ONIMBOTV2REACTIONCHANGE:
// «add», and kept in im_bot_reactions, «delete» when that reaction is taken back; done — ev was such a reaction.
func (s *Service) botV2Reaction(ctx context.Context, b Bot, tok, room string, ev matrix.Event, from imPerson) (bool, error) {
	var target, emoji, action string
	switch ev.Type {
	case "m.reaction":
		rel, _ := ev.Content["m.relates_to"].(map[string]any)
		target, _ = rel["event_id"].(string)
		emoji, _ = rel["key"].(string)
		if !b.Reactions || target == "" || emoji == "" {
			return true, nil
		}
		action = "add"
	case "m.room.redaction":
		gone := ev.Redacts
		if gone == "" {
			gone, _ = ev.Content["redacts"].(string)
		}
		err := s.DB.QueryRow(ctx, `DELETE FROM im_bot_reactions WHERE event_id = $1 AND bot_id::text = $2 RETURNING target, reaction`,
			gone, b.ID).Scan(&target, &emoji)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // not a reaction: a message taken back
		}
		if err != nil || !b.Reactions {
			return true, err
		}
		action = "delete"
	default:
		return false, nil
	}
	msg, err := s.M.EventAs(ctx, tok, room, target)
	if err != nil || msg.Sender != b.mxid {
		return true, nil // not the bot's message (or gone)
	}
	if action == "add" {
		if _, err := s.DB.Exec(ctx, `INSERT INTO im_bot_reactions (event_id, bot_id, room_id, target, reaction) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING`, ev.EventID, b.ID, room, target, emoji); err != nil {
			return true, err
		}
	}
	if r := msg.Unsigned.Relations.Replace; r != nil {
		if nc, ok := r.Content["m.new_content"].(map[string]any); ok {
			msg.Content = nc
		}
	}
	m, err := s.V2Message(ctx, room, msg, b.ID)
	if err != nil {
		return true, err
	}
	chat, err := s.V2Chat(ctx, b, room)
	if err != nil {
		return true, err
	}
	user, err := s.V2Person(ctx, from.ID)
	if err != nil {
		return true, err
	}
	return true, s.QueueV2(ctx, b, BotV2Reaction, map[string]any{"reaction": reactionCode(emoji), "action": action, "message": m,
		"chat": chat, "user": user, "language": botLang()})
}

// BotAround is a message of a chat of the bot with up to n messages before and after it, oldest first (edits shown
// as they are now), their authors' go24 IDs by sender, and whether there are more before and after.
func (s *Service) BotAround(ctx context.Context, botID, room, event string, n int) (evs []matrix.Event, authors map[string]string,
	prev, next bool, err error) {
	_, tok, err := s.botToken(ctx, botID)
	if err != nil {
		return nil, nil, false, false, err
	}
	a, err := s.M.Context(ctx, tok, room, event, 2*n+2)
	if err != nil {
		return nil, nil, false, false, forbidden(err)
	}
	keep := func(list []matrix.Event) []matrix.Event {
		var out []matrix.Event
		for _, ev := range list {
			rel, _ := ev.Content["m.relates_to"].(map[string]any)
			if ev.Type != "m.room.message" || ev.Content["body"] == nil || rel["rel_type"] == "m.replace" {
				continue
			}
			if r := ev.Unsigned.Relations.Replace; r != nil {
				if nc, ok := r.Content["m.new_content"].(map[string]any); ok {
					ev.Content = nc
				}
			}
			out = append(out, ev)
		}
		return out
	}
	before, after := keep(a.Before), keep(a.After)
	slices.Reverse(before)
	prev, next = len(before) > n || len(a.Before) > n, len(after) > n || len(a.After) > n
	if len(before) > n {
		before = before[len(before)-n:]
	}
	if len(after) > n {
		after = after[:n]
	}
	evs = append(append(before, keep([]matrix.Event{a.Event})...), after...)
	senders := make([]string, 0, len(evs))
	for _, ev := range evs {
		senders = append(senders, ev.Sender)
	}
	who, err := s.people(ctx, senders)
	if err != nil {
		return nil, nil, false, false, err
	}
	authors = map[string]string{}
	for _, ev := range evs {
		authors[ev.Sender] = who[ev.Sender].ID
	}
	return evs, authors, prev, next, nil
}

// LiveBot is the bot of the number num that is in the messenger now (err.not_found — none).
func (s *Service) LiveBot(ctx context.Context, num int64) (Bot, error) {
	b, err := scanBot(s.DB.QueryRow(ctx, `SELECT `+botCols+` FROM im_bots b WHERE b.num = $1 AND `+liveBotSQL, num))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, i18n.E("err.not_found")
	}
	return b, err
}

// BotContext gives a bot of imbot.v2 the context of its chat with the employee, opened by a link with BOT_CONTEXT or
// im.v2.Chat.Bot.sendContext (GO24DEV-965): ONIMBOTV2CONTEXTGET with dialogId, context, chat and user.
func (s *Service) BotContext(ctx context.Context, b Bot, userID, room string, context any) error {
	if !b.V2() || b.AppID == "" {
		return nil
	}
	chat, err := s.V2Chat(ctx, b, room)
	if err != nil {
		return err
	}
	user, err := s.V2Person(ctx, userID)
	if err != nil {
		return err
	}
	return s.QueueV2(ctx, b, BotV2ContextGet, map[string]any{"dialogId": chat["dialogId"], "context": context, "chat": chat,
		"user": user, "language": botLang()})
}
