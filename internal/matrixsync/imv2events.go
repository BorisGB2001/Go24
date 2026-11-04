package matrixsync

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/matrix"
)

// The events of im.v2 (GO24DEV-966): an employee subscribed by im.v2.Event.subscribe, go24 worker reads
// their chats as them (as it reads a bot's) and keeps what happens there — a message, its edit, one taken back, a
// reaction put or taken back, someone new in a chat — in the form of im.v2 for 24 hours, until im.v2.Event.get takes
// them by offset (eventPage).

// The events of im.v2.
const (
	IMV2MessageAdd    = "ONIMV2MESSAGEADD"
	IMV2MessageUpdate = "ONIMV2MESSAGEUPDATE"
	IMV2MessageDelete = "ONIMV2MESSAGEDELETE"
	IMV2Reaction      = "ONIMV2REACTIONCHANGE"
	IMV2JoinChat      = "ONIMV2JOINCHAT"
)

// SubscribeV2 starts (on) or stops keeping the events of im.v2 of the employee; stopped, the kept ones go.
func (s *Service) SubscribeV2(ctx context.Context, userID string, on bool) error {
	if on {
		_, err := s.DB.Exec(ctx, `INSERT INTO im_v2_subscriptions (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID)
		return err
	}
	_, err := s.DB.Exec(ctx, `WITH gone AS (DELETE FROM im_v2_subscriptions WHERE user_id::text = $1)
		DELETE FROM im_v2_events WHERE user_id::text = $1`, userID)
	return err
}

// EventsV2 are the employee's events of im.v2 from offset, at most limit (see BotEvents).
func (s *Service) EventsV2(ctx context.Context, userID string, offset int64, limit int) ([]BotEvent, int64, bool, error) {
	return s.eventPage(ctx, "im_v2_events", "user_id", userID, offset, limit)
}

// imV2Loop keeps a sync of the chats of every subscribed employee running, until ctx ends.
func (s *Service) imV2Loop(ctx context.Context) {
	running := map[string]context.CancelFunc{}
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		rows, err := s.DB.Query(ctx, `SELECT s.user_id::text, s.since FROM im_v2_subscriptions s JOIN users u ON u.id = s.user_id AND u.active`)
		var subs map[string]string
		if err == nil {
			subs = map[string]string{}
			for rows.Next() {
				var id, since string
				if err = rows.Scan(&id, &since); err != nil {
					break
				}
				subs[id] = since
			}
			rows.Close()
		}
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Error("matrix: im.v2 subscriptions", "err", err)
			}
		} else {
			for id, since := range subs {
				if running[id] == nil {
					c, cancel := context.WithCancel(ctx)
					running[id] = cancel
					go s.imV2Sync(c, id, since)
				}
			}
			for id, cancel := range running {
				if _, ok := subs[id]; !ok {
					cancel()
					delete(running, id)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// imV2Sync reads the employee's chats as them and keeps what comes as their events; the first sync only takes the
// position (no old history).
func (s *Service) imV2Sync(ctx context.Context, userID, since string) {
	for ctx.Err() == nil {
		me, tok, err := s.token(ctx, userID)
		var res *matrix.UserSync
		if err == nil {
			if res, err = s.M.SyncAs(ctx, tok, since, 30*time.Second, 20); matrix.IsCode(err, "M_UNKNOWN_TOKEN") {
				_, _ = s.DB.Exec(ctx, `UPDATE matrix_users SET token = '' WHERE user_id::text = $1`, userID)
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Warn("matrix: im.v2 sync", "user_id", userID, "err", err)
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
					if err := s.imV2Event(ctx, userID, me, tok, room, ev); err != nil {
						s.Log.Error("matrix: im.v2 event", "user_id", userID, "err", err)
					}
				}
			}
		}
		since = res.NextBatch
		if _, err := s.DB.Exec(ctx, `UPDATE im_v2_subscriptions SET since = $2 WHERE user_id::text = $1`, userID, since); err != nil {
			s.Log.Error("matrix: im.v2 sync position", "err", err)
		}
	}
}

// queueIMV2 keeps an event of im.v2 of the employee.
func (s *Service) queueIMV2(ctx context.Context, userID, event string, data map[string]any) error {
	data["language"] = botLang()
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO im_v2_events (user_id, type, data) SELECT $1::uuid, $2, $3 WHERE EXISTS
		(SELECT 1 FROM im_v2_subscriptions WHERE user_id = $1::uuid)`, userID, event, raw)
	return err
}

// imV2Event turns one event of a chat of the employee (me — their Matrix ID, tok — token) into their event of im.v2.
func (s *Service) imV2Event(ctx context.Context, userID, me, tok, room string, ev matrix.Event) error {
	if ev.Sender == s.M.UserID("go24") && ev.Type != "m.room.member" {
		return nil // go24's notices and system messages
	}
	var who string // whose doing it is (the one who joined, for a member)
	switch {
	case ev.Type == "m.room.member" && ev.StateKey != nil:
		if ev.Content["membership"] != "join" || ev.Unsigned.PrevContent["membership"] == "join" {
			return nil // not someone new: a change of a name, a photo
		}
		who = *ev.StateKey
	case ev.Type == "m.room.message" || ev.Type == "m.reaction" || ev.Type == "m.room.redaction":
		who = ev.Sender
	default:
		return nil
	}
	p, err := s.people(ctx, []string{who})
	if err != nil || p[who].ID == "" {
		return err // someone not in go24
	}
	user, err := s.V2Person(ctx, p[who].ID)
	if err != nil {
		return err
	}
	chat, err := s.v2Chat(ctx, me, tok, room)
	if err != nil {
		return err
	}
	data := map[string]any{"chat": chat, "user": user}
	rel, _ := ev.Content["m.relates_to"].(map[string]any)
	target, _ := rel["event_id"].(string)
	switch ev.Type {
	case "m.room.member":
		data["dialogId"] = chat["dialogId"]
		return s.queueIMV2(ctx, userID, IMV2JoinChat, data)
	case "m.room.message":
		if nc, ok := ev.Content["m.new_content"].(map[string]any); ok && rel["rel_type"] == "m.replace" && target != "" {
			author := p[who].ID
			if data["message"], err = s.V2Message(ctx, room, matrix.Event{EventID: target, Content: map[string]any{"body": nc["body"]}, TS: ev.TS}, author); err != nil {
				return err
			}
			return s.queueIMV2(ctx, userID, IMV2MessageUpdate, data)
		}
		if data["message"], err = s.V2Message(ctx, room, ev, p[who].ID); err != nil {
			return err
		}
		return s.queueIMV2(ctx, userID, IMV2MessageAdd, data)
	case "m.reaction":
		emoji, _ := rel["key"].(string)
		if target == "" || emoji == "" {
			return nil
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO im_v2_reactions (event_id, user_id, target, reaction) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, ev.EventID, userID, target, emoji); err != nil {
			return err
		}
		return s.imV2Reaction(ctx, userID, tok, room, target, emoji, "add", data)
	}
	gone := ev.Redacts // a redaction: a reaction taken back, or a message
	if gone == "" {
		gone, _ = ev.Content["redacts"].(string)
	}
	var emoji string
	err = s.DB.QueryRow(ctx, `DELETE FROM im_v2_reactions WHERE event_id = $1 AND user_id::text = $2 RETURNING target, reaction`,
		gone, userID).Scan(&target, &emoji)
	if err == nil {
		return s.imV2Reaction(ctx, userID, tok, room, target, emoji, "delete", data)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var num int64 // only a message met has a number
	if err := s.DB.QueryRow(ctx, `SELECT num FROM portal_messages WHERE event_id = $1`, gone).Scan(&num); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	data["messageId"] = num
	return s.queueIMV2(ctx, userID, IMV2MessageDelete, data)
}

// imV2Reaction keeps ONIMV2REACTIONCHANGE: the reaction (an emoji) put («add») or taken back on the message target.
func (s *Service) imV2Reaction(ctx context.Context, userID, tok, room, target, emoji, action string, data map[string]any) error {
	msg, err := s.M.EventAs(ctx, tok, room, target)
	if err != nil {
		return nil // gone
	}
	if r := msg.Unsigned.Relations.Replace; r != nil {
		if nc, ok := r.Content["m.new_content"].(map[string]any); ok {
			msg.Content = nc
		}
	}
	author := ""
	if p, err := s.people(ctx, []string{msg.Sender}); err == nil {
		author = p[msg.Sender].ID
	}
	if data["message"], err = s.V2Message(ctx, room, msg, author); err != nil {
		return err
	}
	data["reaction"], data["action"] = reactionCode(emoji), action
	return s.queueIMV2(ctx, userID, IMV2Reaction, data)
}
