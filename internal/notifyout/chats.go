package notifyout

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Chat messages in the messengers pushes them to the phone: a person not in go24 now (no page seen for
// a few minutes) gets the new unread messages of their chats in each linked chat that takes them (the kind
// im_chat, turned off as any other); a reply there (Replied: Telegram) is posted into the go24 chat as theirs.

// ChatKind is the kind of the settings that stands for chat messages (they are no notifications of go24).
const ChatKind = "im_chat"

// ChatSource is go24's messenger (matrixsync), seen from here.
type ChatSource interface {
	// ChatNews: the person's new unread messages since the position ("" — only the current position), the next one.
	ChatNews(ctx context.Context, userID, since string) ([]ChatMessage, string, error)
	// ChatReply posts the text into the chat (into the thread of root, when given) as the person's.
	ChatReply(ctx context.Context, userID, room, root, text string) error
}

// ChatMessage is a new message of someone else in one of the person's chats.
type ChatMessage struct {
	Room, Event, Root string
	Chat              string // "" — a private chat
	Sender, Text      string
}

// TakesChats: the messenger gets chat messages — not e-mail and not the browsers' push, which have no chat to
// answer in.
func TakesChats(c Channel) bool {
	_, direct := c.(Direct)
	_, devices := c.(Devices)
	return !direct && !devices
}

// chatLine is the message as the messenger shows it: «Sender · Chat: text».
func chatLine(m ChatMessage) string {
	head := m.Sender
	if m.Chat != "" {
		head += " · " + m.Chat
	}
	return head + ": " + m.Text
}

// DeliverChats sends the new chat messages of the people with a linked chat that takes them.
func (s *Service) DeliverChats(ctx context.Context, ready map[string]Bot) error {
	if s.Chats == nil || len(ready) == 0 {
		return nil
	}
	rows, err := s.DB.Query(ctx, `SELECT l.user_id::text, l.channel, l.chat, coalesce(c.since, ''),
			coalesce(u.seen_at > now() - interval '3 minutes', false)
		FROM notify_links l JOIN users u ON u.id = l.user_id AND u.active
		LEFT JOIN notify_chat_sync c ON c.user_id = l.user_id
		WHERE l.chat <> '' AND NOT $1 = ANY(l.off) ORDER BY l.user_id`, ChatKind)
	if err != nil {
		return err
	}
	type link struct {
		User, Channel, Chat, Since string
		Online                     bool
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[link])
	if err != nil {
		return err
	}
	for i := 0; i < len(list); {
		j := i
		var links []link
		for ; j < len(list) && list[j].User == list[i].User; j++ {
			if b, ok := ready[list[j].Channel]; ok && TakesChats(s.Channel(b.Channel)) {
				links = append(links, list[j])
			}
		}
		l := list[i]
		i = j
		if len(links) == 0 {
			continue
		}
		news, next, err := s.Chats.ChatNews(ctx, l.User, l.Since)
		if err != nil {
			s.Log.Debug("notify bots: chats", "user", l.User, "err", err) // no Matrix account yet, or Matrix is down
			continue
		}
		if l.Since != "" && !l.Online {
			for _, m := range news {
				for _, k := range links {
					if err := s.sendChat(ctx, ready[k.Channel], k.User, k.Chat, m); err != nil {
						if !errors.Is(err, ErrGone) {
							s.Log.Warn("notify bot: chat message", "channel", k.Channel, "err", plain(err))
							continue
						}
						if _, err := s.DB.Exec(ctx, `DELETE FROM notify_links WHERE user_id = $1 AND channel = $2 AND chat = $3`, k.User, k.Channel, k.Chat); err != nil {
							return err
						}
					}
				}
			}
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO notify_chat_sync (user_id, since) VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET since = EXCLUDED.since`, l.User, next); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM notify_chat_sent WHERE created_at < now() - interval '30 days'`)
	return err
}

// sendChat writes one chat message; a messenger that can be answered keeps what it repeats, for the reply.
func (s *Service) sendChat(ctx context.Context, b Bot, userID, chat string, m ChatMessage) error {
	c := s.Channel(b.Channel)
	msg := Message{Text: chatLine(m), Link: s.AppURL + "/im?room=" + url.QueryEscape(m.Room)}
	r, ok := c.(Replied)
	if !ok {
		return c.Send(ctx, b, chat, msg)
	}
	id, err := r.SendID(ctx, b, chat, msg)
	if err != nil || id == "" {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO notify_chat_sent (channel, chat, msg, user_id, room, event, root) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING`, b.Channel, chat, id, userID, m.Room, m.Event, m.Root)
	return err
}

// chatReply posts a reply to a chat message into its go24 chat; false — it answers none of them.
func (s *Service) chatReply(ctx context.Context, b Bot, u Update) (bool, Message, error) {
	if u.ReplyTo == "" || s.Chats == nil {
		return false, Message{}, nil
	}
	var userID, room, root string
	err := s.DB.QueryRow(ctx, `SELECT s.user_id::text, s.room, s.root FROM notify_chat_sent s
		JOIN notify_links l ON l.user_id = s.user_id AND l.channel = s.channel AND l.chat = s.chat
		WHERE s.channel = $1 AND s.chat = $2 AND s.msg = $3`, b.Channel, u.Chat, u.ReplyTo).Scan(&userID, &room, &root)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, Message{}, nil
	}
	if err != nil {
		return true, Message{}, err
	}
	text := strings.TrimSpace(u.Text)
	if text == "" {
		return true, Message{}, nil
	}
	if err := s.Chats.ChatReply(ctx, userID, room, root, text); err != nil {
		s.Log.Warn("notify bot: chat reply", "channel", b.Channel, "err", plain(err))
		return true, Message{Text: s.Tr("notifyout.bot_reply_failed")}, nil
	}
	return true, Message{}, nil
}
