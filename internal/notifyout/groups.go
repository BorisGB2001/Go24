package notifyout

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// A group chat of go24 linked to a group of a messenger whose bot may be added there (Grouped: Telegram), as a
// bridge (migration 1021): someone who may rename the chat gets a code, adds the company's bot to the group and sends
// /link <code> there. From then on the chat's new messages go to the group as «Sender: text», the group's messages
// come into the chat — as the employee's own when their chat with the bot is linked (in Telegram the private chat's
// id is the person's id), as the bridge bot's with the name otherwise; a reply there to a chat message goes into
// its thread. /unlink in the group or «Отвязать» in go24 ends it.

// GroupSource is go24's messenger as the bridge sees it (matrixsync).
type GroupSource interface {
	// BridgeNews: the new messages of the linked chats since the position ("" — only the current position), the next one.
	BridgeNews(ctx context.Context, since string) ([]ChatMessage, string, error)
	// BridgePost writes a message of the group into the chat: as the employee (userID; "" — none), or with the name.
	BridgePost(ctx context.Context, userID, room, root, name, text string) error
	// BridgeJoin brings the bridge into the chat, invited by the employee; BridgeLeave takes it out.
	BridgeJoin(ctx context.Context, userID, room string) error
	BridgeLeave(ctx context.Context, room string) error
}

// GroupLink is the link of a group chat of go24: the messenger, its bot, the code waiting to be sent in the group
// ("" — none) or the group linked (Title).
type GroupLink struct {
	Channel, Bot string
	Code         string
	Title        string
	Linked       bool
}

// groupBot is the ready bot of a messenger with groups ("" channel — none).
func (s *Service) groupBot(ctx context.Context) (Bot, error) {
	bots, err := s.Bots(ctx)
	if err != nil {
		return Bot{}, err
	}
	for _, b := range bots {
		if _, ok := s.Channel(b.Channel).(Grouped); ok && b.Ready() {
			return b, nil
		}
	}
	return Bot{}, nil
}

// GroupLinkOf is the link of the chat (false — no messenger's bot can be linked to groups, or no bridge).
func (s *Service) GroupLinkOf(ctx context.Context, room string) (GroupLink, bool, error) {
	b, err := s.groupBot(ctx)
	if err != nil || b.Channel == "" || s.Groups == nil {
		return GroupLink{}, false, err
	}
	l := GroupLink{Channel: b.Channel, Bot: b.Name}
	var chat string
	err = s.DB.QueryRow(ctx, `SELECT chat, title, CASE WHEN code_at > now() - $3::interval THEN coalesce(code, '') ELSE '' END
		FROM notify_groups WHERE room = $1 AND channel = $2`, room, b.Channel, codeTTL.String()).Scan(&chat, &l.Title, &l.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, true, nil
	}
	l.Linked = chat != ""
	return l, true, err
}

// StartGroupLink gives the chat a new code for the group (a linked group stays until the code is sent elsewhere).
func (s *Service) StartGroupLink(ctx context.Context, userID, room string) (GroupLink, error) {
	b, err := s.groupBot(ctx)
	if err != nil {
		return GroupLink{}, err
	}
	if b.Channel == "" || s.Groups == nil {
		return GroupLink{}, errNotReady
	}
	raw := make([]byte, 5)
	if _, err := rand.Read(raw); err != nil {
		return GroupLink{}, err
	}
	code := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	_, err = s.DB.Exec(ctx, `INSERT INTO notify_groups (room, channel, code, code_at, user_id) VALUES ($1, $2, $3, now(), $4)
		ON CONFLICT (room) DO UPDATE SET channel = $2, code = $3, code_at = now(), user_id = $4`, room, b.Channel, code, userID)
	if err != nil {
		return GroupLink{}, err
	}
	return s.groupLink(ctx, room)
}

func (s *Service) groupLink(ctx context.Context, room string) (GroupLink, error) {
	l, _, err := s.GroupLinkOf(ctx, room)
	return l, err
}

// UnlinkGroup ends the chat's link to its group; the bridge leaves the chat.
func (s *Service) UnlinkGroup(ctx context.Context, room string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM notify_groups WHERE room = $1`, room)
	if err != nil || tag.RowsAffected() == 0 || s.Groups == nil {
		return err
	}
	if err := s.Groups.BridgeLeave(ctx, room); err != nil {
		s.Log.Warn("notify bot: bridge leave", "room", room, "err", plain(err))
	}
	return nil
}

// groupAnswer takes a message in a group: /link <code> links it, /unlink ends the link, a message of a linked group
// goes into its chat; the bot's answer ("" — none: the bot keeps quiet in groups).
func (s *Service) groupAnswer(ctx context.Context, b Bot, u Update) (Message, error) {
	if u.MovedTo != "" {
		_, err := s.DB.Exec(ctx, `UPDATE notify_groups SET chat = $3 WHERE channel = $1 AND chat = $2`, b.Channel, u.Chat, u.MovedTo)
		return Message{}, err
	}
	if s.Groups == nil {
		return Message{}, nil
	}
	text := strings.TrimSpace(u.Text)
	cmd, arg, _ := strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /link@company_bot in a group of several bots
	switch cmd {
	case "/link":
		return s.groupLinkCode(ctx, b, u, strings.ToLower(strings.TrimSpace(arg)))
	case "/unlink":
		var room string
		err := s.DB.QueryRow(ctx, `SELECT room FROM notify_groups WHERE channel = $1 AND chat = $2`, b.Channel, u.Chat).Scan(&room)
		if errors.Is(err, pgx.ErrNoRows) {
			return Message{}, nil
		}
		if err != nil {
			return Message{}, err
		}
		if err := s.UnlinkGroup(ctx, room); err != nil {
			return Message{}, err
		}
		return Message{Text: s.Tr("notifyout.group_unlinked")}, nil
	}
	if strings.HasPrefix(text, "/") || text == "" {
		return Message{}, nil
	}
	var room string
	err := s.DB.QueryRow(ctx, `SELECT room FROM notify_groups WHERE channel = $1 AND chat = $2`, b.Channel, u.Chat).Scan(&room)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, nil
	}
	if err != nil {
		return Message{}, err
	}
	var root, userID string
	if u.ReplyTo != "" {
		err := s.DB.QueryRow(ctx, `SELECT root FROM notify_group_sent WHERE channel = $1 AND chat = $2 AND msg = $3 AND room = $4`,
			b.Channel, u.Chat, u.ReplyTo, room).Scan(&root)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Message{}, err
		}
	}
	if u.From != "" { // the person's own chat with the bot: they are an employee of go24
		err := s.DB.QueryRow(ctx, `SELECT l.user_id::text FROM notify_links l JOIN users u ON u.id = l.user_id AND u.active
			WHERE l.channel = $1 AND l.chat = $2`, b.Channel, u.From).Scan(&userID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Message{}, err
		}
	}
	if err := s.Groups.BridgePost(ctx, userID, room, root, u.Name, text); err != nil {
		s.Log.Warn("notify bot: group message", "channel", b.Channel, "err", plain(err))
	}
	return Message{}, nil
}

// groupLinkCode links the group to the chat whose code it is; the group linked to another chat before is its no more.
func (s *Service) groupLinkCode(ctx context.Context, b Bot, u Update, code string) (Message, error) {
	var room, userID string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT room, coalesce(user_id::text, '') FROM notify_groups
			WHERE channel = $1 AND code = $2 AND code_at > now() - $3::interval FOR UPDATE`, b.Channel, code, codeTTL.String()).Scan(&room, &userID)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && userID == "" {
			room = ""
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM notify_groups WHERE channel = $1 AND chat = $2 AND room <> $3`, b.Channel, u.Chat, room); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE notify_groups SET chat = $2, title = $3, code = NULL, code_at = NULL, linked_at = now() WHERE room = $1`,
			room, u.Chat, u.Title)
		return err
	})
	if err != nil {
		return Message{}, err
	}
	if room == "" {
		return Message{Text: s.Tr("notifyout.group_bad_code")}, nil
	}
	if err := s.Groups.BridgeJoin(ctx, userID, room); err != nil {
		s.Log.Warn("notify bot: bridge join", "room", room, "err", plain(err))
		_, _ = s.DB.Exec(ctx, `DELETE FROM notify_groups WHERE room = $1`, room)
		return Message{Text: s.Tr("notifyout.group_link_failed")}, nil
	}
	return Message{Text: s.Tr("notifyout.group_linked")}, nil
}

// DeliverGroups sends the linked groups the new messages of their chats.
func (s *Service) DeliverGroups(ctx context.Context, ready map[string]Bot) error {
	if s.Groups == nil {
		return nil
	}
	rows, err := s.DB.Query(ctx, `SELECT room, channel, chat FROM notify_groups WHERE chat <> ''`)
	if err != nil {
		return err
	}
	type link struct{ Room, Channel, Chat string }
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[link])
	if err != nil || len(list) == 0 {
		return err
	}
	links := map[string]link{}
	for _, l := range list {
		links[l.Room] = l
	}
	var since string
	if err := s.DB.QueryRow(ctx, `SELECT since FROM notify_group_sync`).Scan(&since); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	news, next, err := s.Groups.BridgeNews(ctx, since)
	if err != nil {
		s.Log.Debug("notify bots: groups", "err", err) // Matrix is down
		return nil
	}
	for _, m := range news {
		l, ok := links[m.Room]
		b, ready := ready[l.Channel]
		if !ok || !ready || since == "" {
			continue
		}
		if err := s.sendGroup(ctx, b, l.Chat, m); errors.Is(err, ErrGone) {
			if err := s.UnlinkGroup(ctx, l.Room); err != nil {
				return err
			}
			delete(links, l.Room)
		} else if err != nil {
			s.Log.Warn("notify bot: group message", "channel", l.Channel, "err", plain(err))
		}
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO notify_group_sync (since) VALUES ($1) ON CONFLICT (id) DO UPDATE SET since = EXCLUDED.since`, next); err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM notify_group_sent WHERE created_at < now() - interval '30 days'`)
	return err
}

// sendGroup writes one chat message to the group, keeping its id there for a reply.
func (s *Service) sendGroup(ctx context.Context, b Bot, chat string, m ChatMessage) error {
	c := s.Channel(b.Channel)
	msg := Message{Text: m.Sender + ": " + m.Text}
	r, ok := c.(Replied)
	if !ok {
		return c.Send(ctx, b, chat, msg)
	}
	id, err := r.SendID(ctx, b, chat, msg)
	if err != nil || id == "" {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO notify_group_sent (channel, chat, msg, room, event, root) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT DO NOTHING`, b.Channel, chat, id, m.Room, m.Event, m.Root)
	return err
}
