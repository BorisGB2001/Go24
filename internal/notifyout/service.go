package notifyout

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"log/slog"
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
	Text     func(notify.Item) string            // one line of a notification, in the installation's language
	Tr       func(key string, args ...any) string // the bots' answers, in the installation's language
	AppURL   string
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
	errNoChannel = i18n.E("err.notifyout_channel")
	errNotReady  = i18n.E("err.notifyout_not_ready")
	errToken     = i18n.E("err.notifyout_token")
	errServer    = i18n.E("err.notifyout_server")
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
	if !c.NeedsServer() {
		server = ""
	} else if server != "" && !strings.HasPrefix(server, "https://") && !strings.HasPrefix(server, "http://") {
		return errServer
	}
	b := Bot{Channel: channel, Enabled: enabled, Server: server, Name: old.Name, token: strings.TrimSpace(token)}
	if clear {
		b.token, b.Name = "", ""
	} else if b.token == "" {
		b.token = old.token
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
	return err
}

// ---------- an employee's chats ----------

// Link is an employee's chat with a bot of a messenger whose bot is ready.
type Link struct {
	Channel string
	Bot     string          // the bot's name, to look it up in the messenger
	URL     string          // opens the chat with the bot with the code ready ("" — send the code)
	Code    string          // waits to be sent to the bot ("" — linked or not started)
	Account string          // linked: who the person is there
	Linked  bool
	Off     map[string]bool // kinds not sent there
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
		l := Link{Channel: b.Channel, Bot: b.Name, Off: map[string]bool{}}
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

// Unlink forgets the person's chat with the bot of a messenger.
func (s *Service) Unlink(ctx context.Context, userID, channel string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM notify_links WHERE user_id = $1 AND channel = $2`, userID, channel)
	return err
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
}

// read takes the bot's new messages: a code links the chat, /stop unlinks it.
func (s *Service) read(ctx context.Context, b Bot) error {
	c := s.Channel(b.Channel)
	ups, next, err := c.Updates(ctx, b, b.next)
	if err != nil {
		return err
	}
	for _, u := range ups {
		text := strings.TrimSpace(u.Text)
		cmd, arg, _ := strings.Cut(text, " ")
		var answer string
		switch {
		case u.Chat == "":
			continue
		case cmd == "/stop":
			tag, err := s.DB.Exec(ctx, `DELETE FROM notify_links WHERE channel = $1 AND chat = $2`, b.Channel, u.Chat)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				answer = s.Tr("notifyout.bot_unlinked")
			}
		case cmd == "/start" && arg == "":
			answer = s.Tr("notifyout.bot_hello")
		default:
			if cmd == "/start" {
				text = arg
			}
			who, err := s.link(ctx, b.Channel, strings.ToLower(strings.TrimSpace(text)), u)
			if err != nil {
				return err
			}
			if who != "" {
				answer = s.Tr("notifyout.bot_linked", who)
			} else {
				answer = s.Tr("notifyout.bot_bad_code")
			}
		}
		if answer != "" {
			if err := c.Send(ctx, b, u.Chat, Message{Text: answer}); err != nil && !errors.Is(err, ErrGone) {
				s.Log.Warn("notify bot: answer", "channel", b.Channel, "err", plain(err))
			}
		}
	}
	if next != b.next {
		_, err = s.DB.Exec(ctx, `UPDATE notify_bots SET next_update = $2 WHERE channel = $1`, b.Channel, next)
	}
	return err
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
	ready := map[string]Bot{}
	for _, b := range bots {
		if b.Ready() {
			ready[b.Channel] = b
		}
	}
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
	rows, err := s.DB.Query(ctx, `SELECT id, kind, read_at IS NULL AND created_at > now() - interval '1 day'
		FROM notifications WHERE user_id = $1 AND id > $2 ORDER BY id LIMIT 50`, userID, last)
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
	c := s.Channel(b.Channel)
	for _, r := range list {
		if r.Send && !slices.Contains(off, r.Kind) {
			it, err := s.Notify.Get(ctx, userID, strconv.FormatInt(r.ID, 10))
			if err == nil {
				err = c.Send(ctx, b, chat, Message{Text: s.Text(it), Link: s.AppURL + "/notifications/" + strconv.FormatInt(r.ID, 10)})
			} else if errors.Is(err, notify.ErrNotFound) {
				err = nil
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
