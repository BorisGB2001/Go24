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
	"net/url"
	"slices"
)

// Channel is one outside messenger.
type Channel interface {
	Name() string // telegram | max | slack | mattermost …: the locale keys are notifyout.ch.<name>
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
}

// Message is a notification: its line and where it leads ("" — a plain answer of the bot).
type Message struct {
	Text, Link string
}

// ErrGone: the chat is no more — the link is dropped.
var ErrGone = errors.New("notifyout: the chat is gone")

var registered []Channel

// Register adds a messenger (from the init of its module).
func Register(c Channel) { registered = append(registered, c) }

// order is how the messengers are offered; one not named here goes after them.
var order = []string{"telegram", "max", "slack", "mattermost"}

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
