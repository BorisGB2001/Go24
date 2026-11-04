package notifyout

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
	"go24/internal/notify"
)

// Service keeps the bots and the links and delivers the notifications.
type Service struct {
	DB       *pgxpool.Pool
	Channels []Channel // New sets the registered ones
	Notify   *notify.Store
	Text     func(notify.Item) string             // one line of a notification, in the installation's language
	Tr       func(key string, args ...any) string // the bots' answers, in the installation's language
	AppURL   string
	Chats    ChatSource // the people's chat messages for the messengers (nil — none)
	Log      *slog.Logger
	key      []byte
}

// New: secret is go24's own secret (the session secret); the key of the bots' tokens is derived from it.
func New(db *pgxpool.Pool, secret string) *Service {
	k := sha256.Sum256([]byte("go24 notify bots\x00" + secret))
	return &Service{DB: db, Channels: Registered(), key: k[:]}
}

// Channel is the messenger by its name (nil — no such one).
func (s *Service) Channel(name string) Channel {
	for _, c := range s.Channels {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// codeTTL is how long a link code waits for the person to send it to the bot.
const codeTTL = time.Hour

var (
	errNoChannel  = i18n.E("err.notifyout_channel")
	errNotReady   = i18n.E("err.notifyout_not_ready")
	errToken      = i18n.E("err.notifyout_token")
	errServer     = i18n.E("err.notifyout_server")
	errHook       = i18n.E("err.notifyout_hook")
	errServerSMTP = i18n.E("err.notifyout_server_smtp")
	errNoEmail    = i18n.E("err.notifyout_no_email")
	errDevice     = i18n.E("err.notifyout_push_device")
)

// ---------- the bots (administrators) ----------

// Bots are the bots of every messenger, set or not, in the order they are offered.
func (s *Service) Bots(ctx context.Context) ([]Bot, error) {
	rows, err := s.DB.Query(ctx, `SELECT channel, enabled, name, server, secret, next_update FROM notify_bots`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]Bot{}
	for rows.Next() {
		var b Bot
		var sealed []byte
		if err := rows.Scan(&b.Channel, &b.Enabled, &b.Name, &b.Server, &sealed, &b.next); err != nil {
			return nil, err
		}
		if len(sealed) > 0 {
			if b.token, err = s.open(sealed); err != nil {
				s.Log.Warn("notify bot: the token does not open (go24's secret changed?)", "channel", b.Channel)
			}
			b.HasToken = b.token != ""
		}
		set[b.Channel] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Bot, 0, len(s.Channels))
	for _, c := range s.Channels {
		b, ok := set[c.Name()]
		if !ok {
			b = Bot{Channel: c.Name()}
		}
		out = append(out, b)
	}
	return out, nil
}

// Bot is the bot of one messenger.
func (s *Service) Bot(ctx context.Context, channel string) (Bot, error) {
	bots, err := s.Bots(ctx)
	if err != nil {
		return Bot{}, err
	}
	for _, b := range bots {
		if b.Channel == channel {
			return b, nil
		}
	}
	return Bot{}, errNoChannel
}

// SaveBot keeps a bot: a new token (or address) is checked by the messenger first, and the bot's name read from it;
// token "" keeps the one set, clear forgets it.
func (s *Service) SaveBot(ctx context.Context, channel string, enabled bool, server, token string, clear bool, by string) error {
	c := s.Channel(channel)
	if c == nil {
		return errNoChannel
	}
	old, err := s.Bot(ctx, channel)
	if err != nil {
		return err
	}
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if sc, ok := c.(ServerChecked); !c.NeedsServer() {
		server = ""
	} else if ok && server != "" {
		if sc.CheckServer(server) != nil {
			return errServerSMTP
		}
	} else if server != "" && !strings.HasPrefix(server, "https://") && !strings.HasPrefix(server, "http://") {
		return errServer
	}
	b := Bot{Channel: channel, Enabled: enabled, Server: server, Name: old.Name, token: strings.TrimSpace(token)}
	if clear {
		b.token, b.Name = "", ""
	} else if b.token == "" {
		b.token = old.token
	}
	if kc, ok := c.(Keyed); ok {
		// go24 makes the token itself, once, when the channel is turned on; the contact is go24's address
		if b.token = old.token; b.token == "" && enabled {
			if b.token, err = kc.NewToken(); err != nil {
				return err
			}
		}
		b.Server = s.contact()
		server = b.Server
	}
	if b.token != "" && (b.token != old.token || server != old.Server || b.Name == "") {
		name, err := c.Check(ctx, b)
		if err != nil {
			s.Log.Warn("notify bot: check", "channel", channel, "err", err)
			return errToken
		}
		b.Name = name
	}
	var sealed []byte
	if b.token != "" {
		if sealed, err = s.seal(b.token); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO notify_bots (channel, enabled, name, server, secret, updated_by) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (channel) DO UPDATE SET enabled = $2, name = $3, server = $4, secret = $5, updated_by = $6, updated_at = now(),
			next_update = CASE WHEN notify_bots.secret IS DISTINCT FROM $5 THEN 0 ELSE notify_bots.next_update END`,
		channel, enabled, b.Name, server, sealed, by)
	if err != nil {
		return err
	}
	// a messenger that calls go24 itself is told go24's address after the token is kept: it calls there at once
	if hc, ok := c.(Hooked); ok {
		if b.token != "" {
			if err := hc.SetHook(ctx, b, s.AppURL+"/hooks/notify/"+channel); err != nil {
				s.Log.Warn("notify bot: webhook", "channel", channel, "err", plain(err))
				return errHook
			}
		} else if old.token != "" {
			_ = hc.SetHook(ctx, old, "")
		}
	}
	return nil
}

// contact is the company's contact for the push services (RFC 8292): go24's https address, or a mailto: of its host.
func (s *Service) contact() string {
	u, err := url.Parse(s.AppURL)
	if err != nil || u.Host == "" {
		return "mailto:go24@localhost"
	}
	if u.Scheme == "https" {
		return u.Scheme + "://" + u.Host
	}
	return "mailto:go24@" + u.Hostname()
}

// ---------- an employee's chats ----------

// Link is an employee's chat with a bot of a messenger whose bot is ready.
type Link struct {
	Channel string
	Bot     string // the bot's name, to look it up in the messenger
	URL     string // opens the chat with the bot with the code ready ("" — send the code)
	Code    string // waits to be sent to the bot ("" — linked or not started)
	Account string // linked: who the person is there
	Linked  bool
	Off     map[string]bool // kinds not sent there
	Chats   bool            // takes chat messages (TakesChats)
	Key     string          // Devices: what the browser subscribes with (the channel's public key)
	Devices []Device        // Devices: the person's devices
}

// Links are the person's chats, one for each messenger with a ready bot (not started ones too).
func (s *Service) Links(ctx context.Context, userID string) ([]Link, error) {
	bots, err := s.Bots(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT channel, coalesce(code, ''), coalesce(code_at > now() - $2::interval, false), chat, account, off
		FROM notify_links WHERE user_id = $1`, userID, codeTTL.String())
	if err != nil {
		return nil, err
	}
	type row struct {
		Channel, Code string
		Fresh         bool
		Chat, Account string
		Off           []string
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return nil, err
	}
	var out []Link
	for _, b := range bots {
		if !b.Ready() {
			continue
		}
		l := Link{Channel: b.Channel, Bot: b.Name, Off: map[string]bool{}, Chats: TakesChats(s.Channel(b.Channel))}
		if i := slices.IndexFunc(list, func(r row) bool { return r.Channel == b.Channel }); i >= 0 {
			r := list[i]
			l.Linked, l.Account = r.Chat != "", r.Account
			if !l.Linked && r.Code != "" && r.Fresh {
				l.Code, l.URL = r.Code, s.Channel(b.Channel).LinkURL(b, r.Code)
			}
			for _, k := range r.Off {
				l.Off[k] = true
			}
		}
		if _, ok := s.Channel(b.Channel).(Devices); ok {
			l.Key = b.Name
			if l.Devices, err = s.DevicesOf(ctx, userID); err != nil {
				return nil, err
			}
		}
		out = append(out, l)
	}
	return out, nil
}

// StartLink gives the person a new code for the bot of a messenger (a linked chat is replaced once it is sent).
func (s *Service) StartLink(ctx context.Context, userID, channel string) (Link, error) {
	b, err := s.Bot(ctx, channel)
	if err != nil {
		return Link{}, err
	}
	if !b.Ready() {
		return Link{}, errNotReady
	}
	if _, ok := s.Channel(channel).(Direct); ok {
		return s.linkDirect(ctx, userID, b)
	}
	if _, ok := s.Channel(channel).(Devices); ok {
		return Link{}, errDevice
	}
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		return Link{}, err
	}
	code := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	_, err = s.DB.Exec(ctx, `INSERT INTO notify_links (user_id, channel, code, code_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id, channel) DO UPDATE SET code = $3, code_at = now()`, userID, channel, code)
	if err != nil {
		return Link{}, err
	}
	return Link{Channel: channel, Bot: b.Name, Code: code, URL: s.Channel(channel).LinkURL(b, code)}, nil
}

// linkDirect links the person's work address to a Direct channel at once.
func (s *Service) linkDirect(ctx context.Context, userID string, b Bot) (Link, error) {
	var email string
	if err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email); err != nil {
		return Link{}, err
	}
	if email = strings.TrimSpace(email); !strings.Contains(email, "@") {
		return Link{}, errNoEmail
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO notify_links (user_id, channel, chat, account, linked_at, last_id)
		VALUES ($1, $2, $3, $3, now(), coalesce((SELECT max(id) FROM notifications WHERE user_id = $1), 0))
		ON CONFLICT (user_id, channel) DO UPDATE SET code = NULL, code_at = NULL, chat = $3, account = $3, linked_at = now(),
			last_id = excluded.last_id`, userID, b.Channel, email)
	return Link{Channel: b.Channel, Bot: b.Name, Account: email, Linked: true}, err
}

// Unlink forgets the person's chat with the bot of a messenger (and all their devices of a Devices one).
func (s *Service) Unlink(ctx context.Context, userID, channel string) error {
	if _, ok := s.Channel(channel).(Devices); ok {
		if _, err := s.DB.Exec(ctx, `DELETE FROM push_devices WHERE user_id = $1`, userID); err != nil {
			return err
		}
	}
	_, err := s.DB.Exec(ctx, `DELETE FROM notify_links WHERE user_id = $1 AND channel = $2`, userID, channel)
	return err
}

// Device is a device an employee turned push notifications on on.
type Device struct {
	ID, Label, Endpoint string
	Created, Used       *time.Time
}

// DevicesOf are the person's devices, the newest first.
func (s *Service) DevicesOf(ctx context.Context, userID string) ([]Device, error) {
	rows, err := s.DB.Query(ctx, `SELECT id::text, label, endpoint, created_at, used_at FROM push_devices
		WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Device])
}

// AddDevice keeps the person's device (a subscription of the browser to the push channel) and links the channel:
// the notifications from now on go there. The same subscription again (the browser renews it) is kept once; one
// another person had on this browser is theirs no more.
func (s *Service) AddDevice(ctx context.Context, userID string, sub PushSub, label string) error {
	b, err := s.Bot(ctx, "push")
	if err != nil {
		return err
	}
	if !b.Ready() {
		return errNotReady
	}
	if sub.Valid() != nil {
		return errDevice
	}
	if r := []rune(strings.TrimSpace(label)); len(r) > 120 {
		label = string(r[:120])
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO notify_links (user_id, channel, chat, linked_at, last_id)
			VALUES ($1, 'push', 'devices', now(), coalesce((SELECT max(id) FROM notifications WHERE user_id = $1), 0))
			ON CONFLICT (user_id, channel) DO UPDATE SET code = NULL, code_at = NULL, chat = 'devices'`, userID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO push_devices (user_id, endpoint, p256dh, auth, label) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (endpoint) DO UPDATE SET user_id = $1, p256dh = $3, auth = $4, label = $5,
				created_at = CASE WHEN push_devices.user_id = $1 THEN push_devices.created_at ELSE now() END`,
			userID, sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth, strings.TrimSpace(label))
		return err
	})
}

// RemoveDevice forgets one of the person's devices (by its id or its endpoint); the last one gone unlinks the channel.
func (s *Service) RemoveDevice(ctx context.Context, userID, device string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM push_devices WHERE user_id = $1 AND (id::text = $2 OR endpoint = $2)`, userID, device)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM notify_links l WHERE l.user_id = $1 AND l.channel = 'push'
		AND NOT EXISTS (SELECT 1 FROM push_devices d WHERE d.user_id = l.user_id)`, userID)
	return err
}

// TestPush sends the person's devices a trial notification at once; the number of devices it went to.
func (s *Service) TestPush(ctx context.Context, userID string, m Message) (int, error) {
	b, err := s.Bot(ctx, "push")
	if err != nil {
		return 0, err
	}
	if !b.Ready() {
		return 0, errNotReady
	}
	sent := 0
	err = s.toDevices(ctx, b, userID, func(chat string) error { return s.Channel("push").Send(ctx, b, chat, m) }, &sent)
	return sent, err
}

// toDevices calls send for each of the person's devices (their subscription as the chat); a device its push service
// no longer knows is forgotten, the last one gone is ErrGone. sent counts the devices reached.
func (s *Service) toDevices(ctx context.Context, b Bot, userID string, send func(chat string) error, sent *int) error {
	rows, err := s.DB.Query(ctx, `SELECT id::text, endpoint, p256dh, auth FROM push_devices WHERE user_id = $1`, userID)
	if err != nil {
		return err
	}
	type dev struct{ ID, Endpoint, P256dh, Auth string }
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dev])
	if err != nil {
		return err
	}
	left := len(list)
	var firstErr error
	for _, d := range list {
		var sub PushSub
		sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth = d.Endpoint, d.P256dh, d.Auth
		chat, _ := json.Marshal(sub)
		switch err := send(string(chat)); {
		case errors.Is(err, ErrGone):
			if _, err := s.DB.Exec(ctx, `DELETE FROM push_devices WHERE id = $1`, d.ID); err != nil {
				return err
			}
			left--
		case err != nil:
			s.Log.Warn("notify push: send", "err", plain(err))
			if firstErr == nil {
				firstErr = err
			}
		default:
			*sent++
			_, _ = s.DB.Exec(ctx, `UPDATE push_devices SET used_at = now() WHERE id = $1`, d.ID)
		}
	}
	if left == 0 {
		return ErrGone
	}
	if *sent == 0 {
		return firstErr // none reached: the push services are down, the rest waits for the next pass
	}
	return nil
}

// SaveOff keeps the kinds the person does not want in the messengers given (on: channel → the kinds wanted there).
func (s *Service) SaveOff(ctx context.Context, userID string, on map[string]map[string]bool) error {
	all := []string{}
	for _, g := range notify.Kinds {
		all = append(all, g.Kinds...)
	}
	for ch, want := range on {
		off := []string{}
		for _, k := range all {
			if !want[k] {
				off = append(off, k)
			}
		}
		if _, err := s.DB.Exec(ctx, `UPDATE notify_links SET off = $3 WHERE user_id = $1 AND channel = $2`, userID, ch, off); err != nil {
			return err
		}
	}
	return nil
}

// ---------- go24-worker ----------

// Run reads the bots' messages and delivers the notifications every few seconds, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		s.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pass is one round: each ready bot's new messages, then the new notifications.
func (s *Service) Pass(ctx context.Context) {
	bots, err := s.Bots(ctx)
	if err != nil {
		s.Log.Warn("notify bots", "err", err)
		return
	}
	for _, b := range bots {
		if !b.Ready() {
			continue
		}
		if err := s.read(ctx, b); err != nil && ctx.Err() == nil {
			s.Log.Warn("notify bot: messages", "channel", b.Channel, "err", plain(err))
		}
	}
	if err := s.Deliver(ctx, bots); err != nil && ctx.Err() == nil {
		s.Log.Warn("notify bots: delivery", "err", err)
	}
	if err := s.DeliverChats(ctx, readyOf(bots)); err != nil && ctx.Err() == nil {
		s.Log.Warn("notify bots: chat messages", "err", err)
	}
}

// readyOf: the ready bots by their messenger.
func readyOf(bots []Bot) map[string]Bot {
	ready := map[string]Bot{}
	for _, b := range bots {
		if b.Ready() {
			ready[b.Channel] = b
		}
	}
	return ready
}

// read takes the bot's new messages: a code links the chat, /stop unlinks it.
func (s *Service) read(ctx context.Context, b Bot) error {
	c := s.Channel(b.Channel)
	ups, next, err := c.Updates(ctx, b, b.next)
	if err != nil {
		return err
	}
	for _, u := range ups {
		if err := s.take(ctx, b, u); err != nil {
			return err
		}
	}
	if next != b.next {
		_, err = s.DB.Exec(ctx, `UPDATE notify_bots SET next_update = $2 WHERE channel = $1`, b.Channel, next)
	}
	return err
}

// take answers a message to the bot.
func (s *Service) take(ctx context.Context, b Bot, u Update) error {
	m, err := s.answer(ctx, b, u)
	if err != nil || m.Text == "" {
		return err
	}
	if err := s.Channel(b.Channel).Send(ctx, b, u.Chat, m); err != nil && !errors.Is(err, ErrGone) {
		s.Log.Warn("notify bot: answer", "channel", b.Channel, "err", plain(err))
	}
	return nil
}

// answer does what a message to the bot asks: a code links the chat, /stop unlinks it; the bot's answer ("" — none).
// A person who has only opened the chat with a code is asked to send it (a button), as the chat cannot be written
// before they do (Viber).
func (s *Service) answer(ctx context.Context, b Bot, u Update) (Message, error) {
	text := strings.TrimSpace(u.Text)
	cmd, arg, _ := strings.Cut(text, " ")
	switch {
	case u.Chat == "":
		return Message{}, nil
	case cmd == "/stop":
		tag, err := s.DB.Exec(ctx, `DELETE FROM notify_links WHERE channel = $1 AND chat = $2`, b.Channel, u.Chat)
		if err != nil || tag.RowsAffected() == 0 {
			return Message{}, err
		}
		return Message{Text: s.Tr("notifyout.bot_unlinked")}, nil
	case cmd == "/start" && arg == "":
		return Message{Text: s.Tr("notifyout.bot_hello")}, nil
	}
	if ok, m, err := s.chatReply(ctx, b, u); ok || err != nil {
		return m, err
	}
	if cmd == "/start" {
		text = arg
	}
	code := strings.ToLower(strings.TrimSpace(text))
	if u.Opened {
		var ok bool
		err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notify_links WHERE channel = $1 AND code = $2 AND code_at > now() - $3::interval)`,
			b.Channel, code, codeTTL.String()).Scan(&ok)
		if err != nil || !ok {
			return Message{Text: s.Tr("notifyout.bot_bad_code")}, err
		}
		return Message{Text: s.Tr("notifyout.bot_press"), Button: s.Tr("notifyout.link"), Press: code}, nil
	}
	who, err := s.link(ctx, b.Channel, code, u)
	if err != nil {
		return Message{}, err
	}
	if who == "" {
		var linked bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notify_links WHERE channel = $1 AND chat = $2)`, b.Channel, u.Chat).Scan(&linked); err != nil {
			return Message{}, err
		}
		if _, ok := s.Channel(b.Channel).(Replied); ok && linked { // not a code: a message meant for a chat
			return Message{Text: s.Tr("notifyout.bot_reply_hint")}, nil
		}
		return Message{Text: s.Tr("notifyout.bot_bad_code")}, nil
	}
	return Message{Text: s.Tr("notifyout.bot_linked", who)}, nil
}

// Hook takes a call of a messenger that calls go24 itself (Hooked): its messages are answered as the asked ones
// are; the answer to the call itself is the bot's answer to a person who has only opened the chat (nil — none).
// ErrHookSign: not the messenger's call.
func (s *Service) Hook(ctx context.Context, channel string, h http.Header, body []byte) ([]byte, error) {
	hc, ok := s.Channel(channel).(Hooked)
	if !ok {
		return nil, errNoChannel
	}
	b, err := s.Bot(ctx, channel)
	if err != nil {
		return nil, err
	}
	if b.token == "" {
		return nil, ErrHookSign
	}
	ups, err := hc.Hook(b, h, body)
	if err != nil || !b.Ready() {
		return nil, err
	}
	var reply []byte
	for _, u := range ups {
		if !u.Opened {
			if err := s.take(ctx, b, u); err != nil {
				return nil, err
			}
			continue
		}
		m, err := s.answer(ctx, b, u)
		if err != nil {
			return nil, err
		}
		if m.Text != "" {
			reply = hc.Welcome(ctx, b, m)
		}
	}
	return reply, nil
}

// Challenge answers a messenger's check of go24's address (Challenged); ErrHookSign — not the messenger's own.
func (s *Service) Challenge(ctx context.Context, channel string, q url.Values) (string, error) {
	cc, ok := s.Channel(channel).(Challenged)
	if !ok {
		return "", errNoChannel
	}
	b, err := s.Bot(ctx, channel)
	if err != nil {
		return "", err
	}
	if b.token == "" {
		return "", ErrHookSign
	}
	answer, ok := cc.Challenge(b, q)
	if !ok {
		return "", ErrHookSign
	}
	return answer, nil
}

// HookSetup is what the administrators give a Challenged messenger: go24's address for it and the verify token
// ("" — not such a messenger or no token yet).
func (s *Service) HookSetup(b Bot) (addr, verify string) {
	cc, ok := s.Channel(b.Channel).(Challenged)
	if !ok || b.token == "" {
		return "", ""
	}
	return s.AppURL + "/hooks/notify/" + b.Channel, cc.VerifyToken(b)
}

// link ties the chat to the person whose code it is (the name of the person; "" — no such code). The chat linked
// to someone else before is theirs no more: one chat gets one person's notifications.
func (s *Service) link(ctx context.Context, channel, code string, u Update) (string, error) {
	var who string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var userID string
		err := tx.QueryRow(ctx, `SELECT l.user_id::text, u.name FROM notify_links l JOIN users u ON u.id = l.user_id
			WHERE l.channel = $1 AND l.code = $2 AND l.code_at > now() - $3::interval AND u.active FOR UPDATE OF l`,
			channel, code, codeTTL.String()).Scan(&userID, &who)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM notify_links WHERE channel = $1 AND chat = $2 AND user_id <> $3`, channel, u.Chat, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE notify_links SET code = NULL, code_at = NULL, chat = $3, account = $4, linked_at = now(),
				last_id = coalesce((SELECT max(id) FROM notifications WHERE user_id = $1), 0)
			WHERE user_id = $1 AND channel = $2`, userID, channel, u.Chat, u.Account)
		return err
	})
	return who, err
}

// Deliver sends each linked chat the person's notifications after the last one looked at: not those turned off for
// the messenger, not those already read in go24 and not those older than a day (go24-worker was down).
func (s *Service) Deliver(ctx context.Context, bots []Bot) error {
	ready := readyOf(bots)
	if len(ready) == 0 {
		return nil
	}
	rows, err := s.DB.Query(ctx, `SELECT l.user_id::text, l.channel, l.chat, l.off, l.last_id FROM notify_links l
		JOIN users u ON u.id = l.user_id AND u.active
		WHERE l.chat <> '' AND EXISTS (SELECT 1 FROM notifications n WHERE n.user_id = l.user_id AND n.id > l.last_id)`)
	if err != nil {
		return err
	}
	type link struct {
		User, Channel, Chat string
		Off                 []string
		Last                int64
	}
	links, err := pgx.CollectRows(rows, pgx.RowToStructByPos[link])
	if err != nil {
		return err
	}
	for _, l := range links {
		b, ok := ready[l.Channel]
		if !ok {
			continue
		}
		if err := s.deliver(ctx, b, l.User, l.Chat, l.Off, l.Last); err != nil {
			if !errors.Is(err, ErrGone) {
				s.Log.Warn("notify bot: send", "channel", l.Channel, "err", plain(err))
				continue // the messenger is down: the rest waits for the next pass
			}
			if _, err := s.DB.Exec(ctx, `DELETE FROM notify_links WHERE user_id = $1 AND channel = $2 AND chat = $3`, l.User, l.Channel, l.Chat); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) deliver(ctx context.Context, b Bot, userID, chat string, off []string, last int64) error {
	c := s.Channel(b.Channel)
	delay := time.Duration(0)
	if d, ok := c.(Delayed); ok {
		delay = d.Delay()
	}
	rows, err := s.DB.Query(ctx, `SELECT id, kind, read_at IS NULL AND created_at > now() - interval '1 day'
		FROM notifications WHERE user_id = $1 AND id > $2 AND created_at <= now() - $3::interval ORDER BY id LIMIT 50`,
		userID, last, delay.String())
	if err != nil {
		return err
	}
	type row struct {
		ID   int64
		Kind string
		Send bool
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return err
	}
	for _, r := range list {
		if r.Send && !slices.Contains(off, r.Kind) {
			it, err := s.Notify.Get(ctx, userID, strconv.FormatInt(r.ID, 10))
			if errors.Is(err, notify.ErrNotFound) {
				err = nil
			} else if err == nil {
				m := Message{Text: s.Text(it), Link: s.AppURL + "/notifications/" + strconv.FormatInt(r.ID, 10)}
				if _, ok := c.(Devices); ok {
					sent := 0
					err = s.toDevices(ctx, b, userID, func(chat string) error { return c.Send(ctx, b, chat, m) }, &sent)
				} else {
					err = c.Send(ctx, b, chat, m)
				}
			}
			if err != nil {
				return err
			}
		}
		if _, err := s.DB.Exec(ctx, `UPDATE notify_links SET last_id = $3 WHERE user_id = $1 AND channel = $2 AND last_id < $3`,
			userID, b.Channel, r.ID); err != nil {
			return err
		}
	}
	return nil
}

// ---------- the tokens, sealed ----------

func (s *Service) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Service) seal(plain string) ([]byte, error) {
	g, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Service) open(sealed []byte) (string, error) {
	g, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(sealed) < g.NonceSize() {
		return "", errors.New("notifyout: bad token")
	}
	out, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
	return string(out), err
}
