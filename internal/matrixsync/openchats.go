package matrixsync

import (
	"context"
	"sort"
	"strings"

	"go24/internal/i18n"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// Open group chats and channels: their owner makes them open, and then any employee finds them (the
// «Каналы» tab, the search of chats), reads their latest messages and joins without an invitation; a closed one
// takes people by invitation only. In Matrix an open chat's join rule is "restricted" to the company space — its
// members, the employees, join it, guests do not (Element keeps to the same); go24 lists them from im_open_chats.
// A collab's chats are never open: their people are the collab's.

// OpenChat is an open group chat or channel as the list of open chats shows it.
type OpenChat struct {
	ID, Name, Topic string
	Channel         bool
	Members         int
	Joined          bool // the employee is in it
}

// openSpace is the company space whose members join open chats.
func (s *Service) openSpace(ctx context.Context) (string, error) {
	space, err := s.roomOf(ctx, "space", nil)
	if err == nil && space == "" {
		err = i18n.E("err.messenger")
	}
	return space, err
}

func (s *Service) isGuest(ctx context.Context, userID string) bool {
	var g bool
	_ = s.DB.QueryRow(ctx, `SELECT `+tasks.GuestSQL("$1"), userID).Scan(&g)
	return g
}

// SetOpen makes the group chat (or channel) open to every employee (on) or closed again: its owner only.
func (s *Service) SetOpen(ctx context.Context, userID, room string, on bool) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	if pl.Level(me) < ownerLevel || (on && (s.CollabOf(ctx, room) != "" || s.isGuest(ctx, userID))) {
		return i18n.E("err.forbidden")
	}
	return s.setOpen(ctx, userID, tok, room, on)
}

func (s *Service) setOpen(ctx context.Context, userID, tok, room string, on bool) error {
	space := ""
	if on {
		var err error
		if space, err = s.openSpace(ctx); err != nil {
			return err
		}
	}
	if err := s.M.SetJoinRuleAs(ctx, tok, room, space); err != nil {
		return forbidden(err)
	}
	var err error
	if on {
		_, err = s.DB.Exec(ctx, `INSERT INTO im_open_chats (room_id, opened_by) VALUES ($1, $2) ON CONFLICT DO NOTHING`, room, userID)
	} else {
		_, err = s.DB.Exec(ctx, `DELETE FROM im_open_chats WHERE room_id = $1`, room)
	}
	return err
}

// closeOpen closes a chat that goes into a collab (as the token's owner, who may change it).
func (s *Service) closeOpen(ctx context.Context, userID, tok, room string) error {
	var open bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM im_open_chats WHERE room_id = $1)`, room).Scan(&open); err != nil || !open {
		return err
	}
	return s.setOpen(ctx, userID, tok, room, false)
}

// openChat reads an open chat through the admin API; nil — it is not open any more.
func (s *Service) openChat(ctx context.Context, room string) (*OpenChat, error) {
	state, err := s.M.AdminState(ctx, room)
	if err != nil {
		return nil, err
	}
	c := &OpenChat{ID: room}
	open := false
	for _, ev := range state {
		if ev.StateKey == nil {
			continue
		}
		switch ev.Type {
		case "m.room.join_rules":
			open = ev.Content["join_rule"] == "restricted"
		case "m.room.name":
			c.Name, _ = ev.Content["name"].(string)
		case "m.room.topic":
			c.Topic, _ = ev.Content["topic"].(string)
		case "m.room.power_levels":
			c.Channel = isChannel(ev.Content)
		case "m.room.member":
			if ev.Content["membership"] == "join" {
				c.Members++
			}
		}
	}
	if !open {
		return nil, nil
	}
	return c, nil
}

// OpenChats lists the open chats (with a search — those whose name or description has the words), the ones the
// employee is not in first; none for guests.
func (s *Service) OpenChats(ctx context.Context, userID, q string) ([]OpenChat, error) {
	_, tok, err := s.token(ctx, userID)
	if err != nil || s.isGuest(ctx, userID) {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT room_id FROM im_open_chats ORDER BY opened_at DESC`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return nil, err
	}
	joined, err := s.M.JoinedRooms(ctx, tok)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	var out []OpenChat
	for _, id := range ids {
		c, err := s.openChat(ctx, id)
		if err != nil {
			s.Log.Warn("matrix: open chat", "room", id, "err", err)
			continue
		}
		if c == nil || (q != "" && !strings.Contains(strings.ToLower(c.Name+" "+c.Topic), q)) {
			continue
		}
		c.Joined = joined[id]
		out = append(out, *c)
	}
	sort.SliceStable(out, func(i, j int) bool { return !out[i].Joined && out[j].Joined })
	return out, nil
}

// OpenChatInfo is the open chat as the employee sees it before joining; nil — not an open chat (or a guest).
func (s *Service) OpenChatInfo(ctx context.Context, userID, room string) (*OpenChat, error) {
	if s.isGuest(ctx, userID) || !s.listedOpen(ctx, room) {
		return nil, nil
	}
	return s.openChat(ctx, room)
}

func (s *Service) listedOpen(ctx context.Context, room string) bool {
	var listed bool
	_ = s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM im_open_chats WHERE room_id = $1)`, room).Scan(&listed)
	return listed
}

// JoinOpen: the employee joins the open chat «Вступить»; the chat shows «… вступает в чат».
func (s *Service) JoinOpen(ctx context.Context, userID, room string) error {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	if c, err := s.OpenChatInfo(ctx, userID, room); err != nil || c == nil {
		if err == nil {
			err = i18n.E("err.forbidden")
		}
		return err
	}
	return forbidden(s.M.JoinAs(ctx, tok, room))
}

// OpenMessages are an open chat's latest messages for an employee not in it yet (read through the admin API).
func (s *Service) OpenMessages(ctx context.Context, userID, room, from string) ([]Message, string, error) {
	me, _, err := s.token(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if c, err := s.OpenChatInfo(ctx, userID, room); err != nil || c == nil {
		if err == nil {
			err = i18n.E("err.forbidden")
		}
		return nil, "", err
	}
	evs, end, err := s.M.AdminMessages(ctx, room, from, 50)
	if err != nil {
		return nil, "", err
	}
	return s.convert(ctx, evs, room, me, true), end, nil
}

// restrictedJoin: someone joined the chat by its join rule, without an invitation (an open chat).
func restrictedJoin(ev matrix.Event) bool {
	_, ok := ev.Content["join_authorised_via_users_server"]
	prev, _ := ev.Unsigned.PrevContent["membership"].(string)
	return ok && prev != "join" && prev != "invite"
}
