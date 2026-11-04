// Package notifyout sends go24 notifications to outside messengers (GO24DEV-560): Telegram, MAX, Slack,
// Mattermost… Each messenger is a module of its own (a file here) behind the Channel interface, registered in its
// init. The administrators set the company's bot of a messenger (its token, sealed with go24's own secret); an
// employee links their chat with the bot by a code (the bot's link opens the chat with the code ready) and picks
// which notifications go there «Настройки уведомлений» picks them by channel. go24-worker reads what
// people write the bots (the codes, /stop) and delivers the new notifications.
package notifyout

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// Channel is one outside messenger.
type Channel interface {
	Name() string // email | telegram | max | slack | mattermost …: the locale keys are notifyout.ch.<name>
	// NeedsServer: the messenger is the company's own server (Mattermost), its address is set with the token.
	NeedsServer() bool
	// Check reads the bot's own name by its token (an error: the token or the address is wrong).
	Check(ctx context.Context, b Bot) (string, error)
	// Updates are the messages to the bot from the one numbered next on, and the number to ask from the next time.
	Updates(ctx context.Context, b Bot, next int64) ([]Update, int64, error)
	// Send writes to a chat; ErrGone — the chat is no more (the person blocked the bot or left).
	Send(ctx context.Context, b Bot, chat string, m Message) error
	// LinkURL opens the chat with the bot with the code ready ("" — the person sends the code themselves).
	LinkURL(b Bot, code string) string
}

// Hooked is a messenger that calls go24 itself (a webhook) with the messages to the bot instead of being asked for
// them (Viber): its Updates give none. go24 needs a public https address for it.
type Hooked interface {
	// SetHook gives the messenger go24's address for its calls ("" — no calls any more).
	SetHook(ctx context.Context, b Bot, url string) error
	// Hook reads a call: the messages in it; ErrHookSign — not the messenger's call.
	Hook(b Bot, h http.Header, body []byte) ([]Update, error)
	// Welcome is the answer to the call of a person who has only opened the chat (Update.Opened).
	Welcome(ctx context.Context, b Bot, m Message) []byte
}

// Replied is a messenger whose messages can be answered: Send gives the message's id there, an Update carries the id
// of the message it answers (Telegram: reply_to_message), so a reply to a chat message goes into that go24 chat.
type Replied interface {
	SendID(ctx context.Context, b Bot, chat string, m Message) (string, error)
}

// Challenged is a Hooked messenger that checks go24's address with a GET first, by a verify token the
// administrators give it with the address (WhatsApp: Meta's hub.challenge).
type Challenged interface {
	VerifyToken(b Bot) string
	// Challenge is the answer to the check (false — not the messenger's own).
	Challenge(b Bot, q url.Values) (string, bool)
}

// Direct is a channel the person needs no chat with a bot for (e-mail): «Подключить» links their work address at
// once, without a code.
type Direct interface{ Direct() }

// Delayed is a channel that gets a notification only when it is still unread in go24 after Delay (e-mail: no letter
// for what the person has seen on the portal).
type Delayed interface{ Delay() time.Duration }

// ServerChecked is a channel whose server address is not a web one (e-mail: smtps://…): it checks the address itself.
type ServerChecked interface{ CheckServer(server string) error }

// ErrHookSign: a call to go24's address for a messenger is not the messenger's own.
var ErrHookSign = errors.New("notifyout: not the messenger's call")

// Bot is the company's bot of a messenger as the administrators set it; the token is never shown back.
type Bot struct {
	Channel  string
	Enabled  bool
	Name     string // the bot's own name in the messenger (t.me/<name>)
	Server   string
	HasToken bool
	token    string
	next     int64
}

// Token is the bot's token, for the channel modules.
func (b Bot) Token() string { return b.token }

// Ready: employees may link their chats.
func (b Bot) Ready() bool { return b.Enabled && b.token != "" }

// Update is a message to the bot.
type Update struct {
	Chat    string // where to answer and send notifications
	Account string // who wrote it, as the messenger names them (@name)
	Text    string
	Opened  bool   // the person has only opened the chat (Hooked): it cannot be written yet, the answer goes in the call's answer
	ReplyTo string // the id of the bot's message it answers (Replied)
}

// Message is a notification: its line and where it leads ("" — a plain answer of the bot).
type Message struct {
	Text, Link string
	Button     string // a button under the answer that sends Press to the bot ("" — none)
	Press      string
}

// ErrGone: the chat is no more — the link is dropped.
var ErrGone = errors.New("notifyout: the chat is gone")

var registered []Channel

// Register adds a messenger (from the init of its module).
func Register(c Channel) { registered = append(registered, c) }

// order is how the messengers are offered; one not named here goes after them.
var order = []string{"push", "email", "telegram", "max", "matrix", "vk", "viber", "whatsapp", "slack", "mattermost"}

// Registered are the messengers in the order they are offered.
func Registered() []Channel {
	list := slices.Clone(registered)
	rank := func(c Channel) int {
		if i := slices.Index(order, c.Name()); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(list, func(a, b Channel) int { return rank(a) - rank(b) })
	return list
}

// plain strips the address from an HTTP error: the bot's token is part of the address of some messengers' API.
func plain(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return errors.New(ue.Op + ": " + ue.Err.Error())
	}
	return err
}
