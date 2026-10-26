package matrixsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// Chat bots of applications, as imbot.* (migration 329): a bot is a participant of the messenger with its
// own Matrix account, not an employee — people write to it in a private chat or add it to a group chat. go24 worker
// reads the bot's chats and queues the events to the application's handlers (app_events): ONIMBOTMESSAGEADD — a
// message to the bot (every one in a private chat; in a group chat when the bot is mentioned or answered, every one
// for a bot of type S), ONIMBOTJOINCHAT — someone opened a chat with the bot or added it to a group chat,
// ONIMBOTDELETE — the bot is unregistered. The application answers as the bot: Send, Edit, Delete with the bot's ID.

// Bot is a chat bot of an application.
type Bot struct {
	ID             string    `json:"id"`
	AppID          string    `json:"-"`
	Code           string    `json:"code"`
	Type           string    `json:"type"` // B — a bot; H — a bot answering as a person would; S — it gets every message of its group chats
	Name           string    `json:"name"`
	LastName       string    `json:"last_name"`
	WorkPosition   string    `json:"work_position"`
	Color          string    `json:"color"`
	MessageHandler string    `json:"event_message_add"`
	WelcomeHandler string    `json:"event_welcome_message"`
	DeleteHandler  string    `json:"event_bot_delete"`
	UserID         string    `json:"user_id"` // who registered it
	CreatedAt      time.Time `json:"created_at"`
	mxid, since    string
}

// FullName is how the messenger shows the bot.
func (b Bot) FullName() string { return strings.TrimSpace(b.Name + " " + b.LastName) }

// The bot events of applications (apps.Events has them too).
const (
	BotMessageAdd = "ONIMBOTMESSAGEADD"
	BotJoinChat   = "ONIMBOTJOINCHAT"
	BotDelete     = "ONIMBOTDELETE"
)

var errBotCode = i18n.E("err.imbot_code")

const botCols = `b.id::text, coalesce(b.app_id::text, ''), b.code, b.type, b.name, b.last_name, b.work_position, b.color,
	b.message_handler, b.welcome_handler, b.delete_handler, coalesce(b.user_id::text, ''), b.created_at, b.mxid, b.since`

func scanBot(r pgx.Row) (Bot, error) {
	var b Bot
	err := r.Scan(&b.ID, &b.AppID, &b.Code, &b.Type, &b.Name, &b.LastName, &b.WorkPosition, &b.Color,
		&b.MessageHandler, &b.WelcomeHandler, &b.DeleteHandler, &b.UserID, &b.CreatedAt, &b.mxid, &b.since)
	return b, err
}

// normBot checks what the application gives for a bot.
func normBot(b *Bot) error {
	b.Code, b.Name, b.LastName = strings.TrimSpace(b.Code), strings.TrimSpace(b.Name), strings.TrimSpace(b.LastName)
	b.WorkPosition, b.Color = strings.TrimSpace(b.WorkPosition), strings.ToUpper(strings.TrimSpace(b.Color))
	if b.Type = strings.ToUpper(strings.TrimSpace(b.Type)); b.Type == "" || b.Type == "O" { // O — a bot of open lines: as B here
		b.Type = "B"
	}
	if b.Type != "B" && b.Type != "H" && b.Type != "S" {
		return i18n.E("err.imbot_type", b.Type)
	}
	if b.Name == "" || len([]rune(b.Name)) > 100 {
		return i18n.E("err.imbot_name")
	}
	return nil
}

// RegisterBot makes a bot of the application (imbot.register) with its Matrix account; the handlers must be bound to
// the bot events already (apps.BindEvent). The same code again is an error.
func (s *Service) RegisterBot(ctx context.Context, appID, userID string, b Bot) (Bot, error) {
	if err := normBot(&b); err != nil {
		return b, err
	}
	n := make([]byte, 6)
	if _, err := rand.Read(n); err != nil {
		return b, err
	}
	b.mxid = s.M.UserID("bot." + hex.EncodeToString(n))
	row := s.DB.QueryRow(ctx, `INSERT INTO im_bots AS b (app_id, code, type, name, last_name, work_position, color, mxid, user_id,
		message_handler, welcome_handler, delete_handler) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9, '')::uuid, $10, $11, $12)
		ON CONFLICT DO NOTHING RETURNING `+botCols, appID, b.Code, b.Type, b.Name, b.LastName, b.WorkPosition, b.Color, b.mxid, userID,
		b.MessageHandler, b.WelcomeHandler, b.DeleteHandler)
	out, err := scanBot(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, errBotCode
	}
	if err != nil {
		return b, err
	}
	if err := s.M.PutUser(ctx, out.mxid, out.FullName(), "", "", false); err != nil {
		_, _ = s.DB.Exec(ctx, `DELETE FROM im_bots WHERE id = $1`, out.ID)
		return b, err
	}
	return out, nil
}

// UpdateBot changes the bot's name, position, type and handlers (imbot.update); empty fields keep what it has.
func (s *Service) UpdateBot(ctx context.Context, appID, id string, ch Bot) (Bot, error) {
	b, err := s.AppBot(ctx, appID, id)
	if err != nil {
		return b, err
	}
	for _, f := range []struct{ to, v *string }{{&b.Code, &ch.Code}, {&b.Type, &ch.Type}, {&b.Name, &ch.Name}, {&b.LastName, &ch.LastName},
		{&b.WorkPosition, &ch.WorkPosition}, {&b.Color, &ch.Color}, {&b.MessageHandler, &ch.MessageHandler},
		{&b.WelcomeHandler, &ch.WelcomeHandler}, {&b.DeleteHandler, &ch.DeleteHandler}} {
		if strings.TrimSpace(*f.v) != "" {
			*f.to = *f.v
		}
	}
	if err := normBot(&b); err != nil {
		return b, err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE im_bots SET code = $2, type = $3, name = $4, last_name = $5, work_position = $6, color = $7,
		message_handler = $8, welcome_handler = $9, delete_handler = $10 WHERE id::text = $1 AND app_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM im_bots o WHERE o.app_id = im_bots.app_id AND upper(o.code) = upper($2) AND o.id <> im_bots.id)`,
		b.ID, b.Code, b.Type, b.Name, b.LastName, b.WorkPosition, b.Color, b.MessageHandler, b.WelcomeHandler, b.DeleteHandler)
	if err != nil {
		return b, err
	}
	if tag.RowsAffected() == 0 {
		return b, errBotCode
	}
	return b, s.M.PutUser(ctx, b.mxid, b.FullName(), "", "", false)
}

// DeleteBot unregisters the bot (imbot.unregister): its handler hears ONIMBOTDELETE, the worker closes its account
// (it leaves its chats, its messages stay).
func (s *Service) DeleteBot(ctx context.Context, appID, id string) error {
	b, err := s.AppBot(ctx, appID, id)
	if err != nil {
		return err
	}
	if err := s.botQueue(ctx, b, BotDelete, b.DeleteHandler, map[string]any{"BOT_ID": b.ID, "BOT_CODE": b.Code}); err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE im_bots SET app_id = NULL WHERE id::text = $1`, b.ID)
	return err
}

// AppBot is a bot of the application by its ID or, failing that, its code.
func (s *Service) AppBot(ctx context.Context, appID, id string) (Bot, error) {
	b, err := scanBot(s.DB.QueryRow(ctx, `SELECT `+botCols+` FROM im_bots b WHERE b.app_id::text = $1
		AND (b.id::text = $2 OR upper(b.code) = upper($2)) ORDER BY b.id::text = $2 DESC LIMIT 1`, appID, strings.TrimSpace(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, i18n.E("err.not_found")
	}
	return b, err
}

// Bots are the bots of the application (imbot.bot.list), or of every installed application with appID "".
func (s *Service) Bots(ctx context.Context, appID string) ([]Bot, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+botCols+` FROM im_bots b JOIN apps a ON a.id = b.app_id
		WHERE ($1 = '' OR b.app_id::text = $1) ORDER BY b.created_at, b.code`, appID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Bot, error) { return scanBot(r) })
}

// botPuppet is puppet for a bot: its Matrix ID and token (logged in on first use); none for a bot that is gone.
func (s *Service) botPuppet(ctx context.Context, id string) (mxid, token string, err error) {
	err = s.DB.QueryRow(ctx, `SELECT mxid, token FROM im_bots WHERE id::text = $1 AND app_id IS NOT NULL`, id).Scan(&mxid, &token)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil || token != "" {
		return mxid, token, err
	}
	if token, err = s.M.LoginAs(ctx, mxid); err != nil {
		return "", "", err
	}
	_, err = s.DB.Exec(ctx, `UPDATE im_bots SET token = $2 WHERE id::text = $1`, id, token)
	return mxid, token, err
}

// botQueue queues an event of the bot to its handler, when it has one and the application is installed.
func (s *Service) botQueue(ctx context.Context, b Bot, event, handler string, data map[string]any) error {
	if handler == "" || b.AppID == "" {
		return nil
	}
	data["BOT"] = map[string]any{b.ID: map[string]any{"BOT_ID": b.ID, "BOT_CODE": b.Code}}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `INSERT INTO app_event_deliveries (event_id, event, data) SELECT e.id, $3, $4 FROM app_events e
		JOIN apps a ON a.id = e.app_id WHERE e.app_id::text = $1 AND e.event = $3 AND e.handler_url = $2 AND a.installed_at IS NOT NULL`,
		b.AppID, handler, event, raw)
	if err == nil && tag.RowsAffected() > 0 && s.BotQueued != nil {
		s.BotQueued()
	}
	return err
}

// ---------- the worker: the bots' chats ----------

// botLoop keeps one sync for each bot of an installed application and closes the accounts of the bots that are gone,
// until ctx ends.
func (s *Service) botLoop(ctx context.Context) {
	running := map[string]context.CancelFunc{}
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		if bots, err := s.Bots(ctx, ""); err != nil {
			if ctx.Err() == nil {
				s.Log.Error("matrix: bots", "err", err)
			}
		} else {
			live := map[string]bool{}
			for _, b := range bots {
				live[b.ID] = true
				if running[b.ID] == nil {
					c, cancel := context.WithCancel(ctx)
					running[b.ID] = cancel
					go s.botSync(c, b)
				}
			}
			for id, cancel := range running {
				if !live[id] {
					cancel()
					delete(running, id)
				}
			}
		}
		if err := s.closeBots(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("matrix: close bots", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// closeBots deactivates the Matrix accounts of the bots that are gone (unregistered, their application taken off).
func (s *Service) closeBots(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT id::text, mxid FROM im_bots WHERE app_id IS NULL AND closed_at IS NULL`)
	if err != nil {
		return err
	}
	type gone struct{ id, mxid string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (gone, error) {
		var g gone
		return g, r.Scan(&g.id, &g.mxid)
	})
	if err != nil {
		return err
	}
	for _, g := range list {
		if u, err := s.M.GetUser(ctx, g.mxid); err != nil {
			return err
		} else if u != nil && !u.Deactivated {
			if err := s.M.Deactivate(ctx, g.mxid); err != nil {
				return err
			}
		}
		// the events already queued stay: they are the application's; the row stays for the bot's name on its messages
		if _, err := s.DB.Exec(ctx, `UPDATE im_bots SET closed_at = now(), token = '' WHERE id::text = $1 AND app_id IS NULL`, g.id); err != nil {
			return err
		}
	}
	return nil
}

// botSync reads the bot's chats as the bot and turns what comes into its events; the first sync only takes the
// position (no old history).
func (s *Service) botSync(ctx context.Context, b Bot) {
	since := b.since
	for ctx.Err() == nil {
		_, tok, err := s.botPuppet(ctx, b.ID)
		if err == nil && tok == "" {
			return // gone
		}
		var res *matrix.UserSync
		if err == nil {
			res, err = s.M.SyncAs(ctx, tok, since, 30*time.Second, 20)
			if matrix.IsCode(err, "M_UNKNOWN_TOKEN") {
				_, _ = s.DB.Exec(ctx, `UPDATE im_bots SET token = '' WHERE id::text = $1`, b.ID)
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Warn("matrix: bot sync", "bot", b.Code, "err", err)
				select {
				case <-ctx.Done():
				case <-time.After(10 * time.Second):
				}
			}
			continue
		}
		if since != "" {
			for room, r := range res.Rooms.Join {
				for _, ev := range r.Timeline.Events {
					if err := s.botEvent(ctx, b.ID, room, ev); err != nil {
						s.Log.Error("matrix: bot event", "bot", b.Code, "err", err)
					}
				}
			}
		}
		since = res.NextBatch
		if _, err := s.DB.Exec(ctx, `UPDATE im_bots SET since = $2 WHERE id::text = $1`, b.ID, since); err != nil {
			s.Log.Error("matrix: bot sync position", "err", err)
		}
	}
}

// botEvent turns one event of a bot's chat into its event for the application: a message to the bot, the bot
// joining a chat it was invited into (a private chat opened with it, a group chat it was added to).
func (s *Service) botEvent(ctx context.Context, botID, room string, ev matrix.Event) error {
	b, err := scanBot(s.DB.QueryRow(ctx, `SELECT `+botCols+` FROM im_bots b JOIN apps a ON a.id = b.app_id
		WHERE b.id::text = $1 AND a.installed_at IS NOT NULL`, botID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, tok, err := s.botPuppet(ctx, b.ID)
	if err != nil || tok == "" {
		return err
	}
	switch {
	case ev.Type == "m.room.member" && ev.StateKey != nil && *ev.StateKey == b.mxid:
		// joined after an invitation: not its own new chat (it writes first), not a change of its name
		if ev.Content["membership"] != "join" || ev.Unsigned.PrevContent["membership"] != "invite" {
			return nil
		}
		mem, err := s.M.Members(ctx, room, tok)
		if err != nil {
			return err
		}
		private, peer := s.botPeer(ctx, b.mxid, mem)
		if private && peer.ID == "" {
			return nil
		}
		params := map[string]any{"DIALOG_ID": room, "CHAT_TYPE": "C", "MESSAGE_TYPE": "C", "CHAT_ID": room}
		if private {
			params["DIALOG_ID"], params["CHAT_TYPE"], params["MESSAGE_TYPE"], params["USER_ID"] = peer.ID, "P", "P", peer.ID
		}
		data := map[string]any{"PARAMS": params}
		if private {
			data["USER"] = botUser(peer)
		}
		return s.botQueue(ctx, b, BotJoinChat, b.WelcomeHandler, data)
	case ev.Type == "m.room.message" && ev.Sender != b.mxid && ev.Sender != s.M.UserID("go24"):
		body, _ := ev.Content["body"].(string)
		mt, _ := ev.Content["msgtype"].(string)
		if rel, _ := ev.Content["m.relates_to"].(map[string]any); rel["rel_type"] == "m.replace" || body == "" || mt == "m.notice" {
			return nil // an edit, a message taken back, a notice of go24
		}
		who, err := s.people(ctx, []string{ev.Sender})
		if err != nil {
			return err
		}
		from, ok := who[ev.Sender]
		if !ok || from.Bot || from.ID == "" {
			return nil // someone not in go24, or another bot (bots do not talk to each other)
		}
		mem, err := s.M.Members(ctx, room, tok)
		if err != nil {
			return err
		}
		private, _ := s.botPeer(ctx, b.mxid, mem)
		if !private && b.Type != "S" && !mentions(ev.Content, b.mxid) && !s.answers(ctx, tok, room, ev, b.mxid) {
			return nil
		}
		if cmd, err := s.botHasCommand(ctx, b.ID, body); err != nil || cmd {
			return err // a command of the bot: it goes to the command's handler (botCommands)
		}
		params := botParams(b, room, private, from, ev.EventID, stripReplyFallback(body))
		if rel, _ := ev.Content["m.relates_to"].(map[string]any); rel["rel_type"] == "m.thread" {
			params["THREAD_ID"] = rel["event_id"]
		}
		if f := fileOf(ev.Content); f != nil {
			params["FILES"] = []map[string]any{{"name": f.Name, "type": f.Kind, "size": f.Size, "mime": f.Mime}}
		}
		return s.botQueue(ctx, b, BotMessageAdd, b.MessageHandler, map[string]any{"PARAMS": params, "USER": botUser(from)})
	}
	return nil
}

// botPeer: the chat is the bot's private chat with one person (who); a group chat otherwise.
func (s *Service) botPeer(ctx context.Context, botMXID string, mem map[string]string) (bool, imPerson) {
	if len(mem) != 2 {
		return false, imPerson{}
	}
	for mx := range mem {
		if mx != botMXID {
			who, err := s.people(ctx, []string{mx})
			if err != nil {
				return false, imPerson{}
			}
			return true, who[mx]
		}
	}
	return false, imPerson{}
}

// answers: the message answers (quotes) a message of the bot.
func (s *Service) answers(ctx context.Context, tok, room string, ev matrix.Event, botMXID string) bool {
	rel, _ := ev.Content["m.relates_to"].(map[string]any)
	in, _ := rel["m.in_reply_to"].(map[string]any)
	id, _ := in["event_id"].(string)
	if id == "" || rel["is_falling_back"] == true {
		return false
	}
	q, err := s.M.EventAs(ctx, tok, room, id)
	return err == nil && q.Sender == botMXID
}

// botUser is USER of a bot event: the employee who wrote describes them.
func botUser(p imPerson) map[string]any {
	yn := func(b bool) string {
		if b {
			return "Y"
		}
		return "N"
	}
	return map[string]any{"ID": p.ID, "NAME": p.Name, "IS_BOT": yn(p.Bot), "IS_EXTRANET": yn(p.Guest)}
}

// BotDialog is the room of a bot's dialog (DIALOG_ID of imbot.message.add): a Matrix room the bot is in as is, an
// employee's ID — the private chat with them (made when there is none yet).
func (s *Service) BotDialog(ctx context.Context, botID, dialog string) (string, error) {
	dialog = strings.TrimSpace(dialog)
	if strings.HasPrefix(dialog, "!") {
		return dialog, nil
	}
	return s.DirectChat(ctx, botID, dialog)
}

// ChatBots are the bots the employee may write to (a guest — none), for the messenger's people picker.
func (s *Service) ChatBots(ctx context.Context, userID string) ([]Bot, error) {
	var guest bool
	if err := s.DB.QueryRow(ctx, `SELECT `+tasks.GuestSQL("$1"), userID).Scan(&guest); err != nil || guest {
		return nil, err
	}
	return s.Bots(ctx, "")
}
