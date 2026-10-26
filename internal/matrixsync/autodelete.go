package matrixsync

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/matrix"
)

// Auto-delete of a chat's messages: in a private chat either of the two, in a group chat its owner
// (and a channel's admins) choose how long a message lives — an hour, a day, a week, a month; the messages sent after
// that go for everyone when their time is up, and the chat shows a line about who turned it on or off. In Matrix it
// is the room's go24.autodelete state ({"ttl": seconds, 0 — off}); deleting is a redaction, as «Удалить» does.

const autoDeleteType = "go24.autodelete"

// AutoDeleteTTLs are the lifetimes to choose from, in seconds: an hour, a day, a week, a month.
var AutoDeleteTTLs = []int{3600, 86400, 7 * 86400, 30 * 86400}

// AutoDelete gives the chat's auto-delete (seconds, 0 — off) and whether the employee may change it: a private or
// group chat (not go24's own) where their power level lets them put its state.
func (s *Service) AutoDelete(ctx context.Context, userID, room string) (ttl int, can bool, err error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return 0, false, err
	}
	var st struct {
		TTL int `json:"ttl"`
	}
	if err := s.M.StateAs(ctx, tok, room, autoDeleteType, "", &st); err != nil && !matrix.IsCode(err, "M_NOT_FOUND") {
		return 0, false, forbidden(err)
	}
	own, err := s.ownRoom(ctx, room)
	if err != nil || own {
		return st.TTL, false, err
	}
	pl := matrix.PowerLevels{}
	if err := s.M.StateAs(ctx, tok, room, "m.room.power_levels", "", &pl); err != nil {
		return st.TTL, false, nil
	}
	return st.TTL, pl.Level(me) >= stateLevel(pl, autoDeleteType), nil
}

// SetAutoDelete turns the chat's auto-delete on (ttl — one of AutoDeleteTTLs) or off (0).
func (s *Service) SetAutoDelete(ctx context.Context, userID, room string, ttl int) error {
	if ttl != 0 && !slices.Contains(AutoDeleteTTLs, ttl) {
		return i18n.E("err.forbidden")
	}
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	if own, err := s.ownRoom(ctx, room); err != nil || own {
		return errors.Join(err, errNotGroup)
	}
	if err := s.M.SetStateAs(ctx, tok, room, autoDeleteType, "", map[string]any{"ttl": ttl}); err != nil {
		return forbidden(err)
	}
	if ttl == 0 {
		_, err = s.DB.Exec(ctx, `DELETE FROM im_autodelete WHERE room_id = $1`, room)
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO im_autodelete (room_id, ttl, user_id) VALUES ($1, $2, $3)
		ON CONFLICT (room_id) DO UPDATE SET ttl = $2, user_id = $3`, room, ttl, userID)
	return err
}

// collabAutoDelete puts a collab's «Автоудаление сообщений» (its settings) into the collab's chat: the room's
// go24.autodelete state, written by go24 with whom the change is from (the chat's line names them), and the worker's
// index, deleting first as that person (userID; "" — nobody to name, as for a collab of before).
func (s *Service) collabAutoDelete(ctx context.Context, room string, ttl int, userID string) error {
	var had int // the index follows the state: when it says the same, so does the room
	var hadBy string
	if err := s.DB.QueryRow(ctx, `SELECT ttl, user_id::text FROM im_autodelete WHERE room_id = $1`, room).Scan(&had, &hadBy); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if had == ttl && (ttl == 0 || hadBy == userID || userID == "") {
		return nil
	}
	var st struct {
		TTL int `json:"ttl"`
	}
	if err := s.M.State(ctx, room, autoDeleteType, "", &st); err != nil && !matrix.IsCode(err, "M_NOT_FOUND") {
		return err
	}
	if st.TTL != ttl {
		var by string
		if err := s.DB.QueryRow(ctx, `SELECT mxid FROM matrix_users WHERE user_id::text = $1 AND mxid <> ''`, userID).Scan(&by); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := s.M.SetState(ctx, room, autoDeleteType, "", map[string]any{"ttl": ttl, "by": by}); err != nil {
			return err
		}
	}
	if ttl == 0 || userID == "" {
		_, err := s.DB.Exec(ctx, `DELETE FROM im_autodelete WHERE room_id = $1`, room)
		return err
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO im_autodelete (room_id, ttl, user_id) VALUES ($1, $2, $3)
		ON CONFLICT (room_id) DO UPDATE SET ttl = $2, user_id = $3 WHERE im_autodelete.ttl <> $2 OR im_autodelete.user_id <> $3`, room, ttl, userID)
	return err
}

// ownRoom: the room is one of go24's own chats (a project's, a task's, the company's) — no auto-delete there.
func (s *Service) ownRoom(ctx context.Context, room string) (bool, error) {
	var kind string
	err := s.DB.QueryRow(ctx, `SELECT kind FROM matrix_rooms WHERE room_id = $1`, room).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// stateLevel is the power level a state event of the type needs.
func stateLevel(pl matrix.PowerLevels, typ string) int {
	if events, ok := pl["events"].(map[string]any); ok {
		if v, ok := events[typ].(float64); ok {
			return int(v)
		}
	}
	if v, ok := pl["state_default"].(float64); ok {
		return int(v)
	}
	return 50
}

// autoDeleteLoop deletes the messages whose time is up, every minute, until ctx ends.
func (s *Service) autoDeleteLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.expire(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.Log.Warn("matrix: auto-delete", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// expire redacts, in each chat with auto-delete, the messages sent since it was turned on and older than its ttl at
// now — as the one who turned it on (they may delete the others' messages), else as the message's author.
func (s *Service) expire(ctx context.Context, now time.Time) error {
	rows, err := s.DB.Query(ctx, `SELECT room_id, ttl, since, swept, user_id FROM im_autodelete`)
	if err != nil {
		return err
	}
	type chat struct {
		room, user   string
		ttl          int
		since, swept time.Time
	}
	var chats []chat
	for rows.Next() {
		var c chat
		var swept *time.Time
		if err := rows.Scan(&c.room, &c.ttl, &c.since, &swept, &c.user); err != nil {
			rows.Close()
			return err
		}
		if c.swept = c.since; swept != nil && swept.After(c.since) {
			c.swept = *swept
		}
		chats = append(chats, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range chats {
		cutoff := now.Add(-time.Duration(c.ttl) * time.Second)
		if !cutoff.After(c.swept) {
			continue
		}
		if err := s.expireRoom(ctx, c.room, c.user, c.swept, cutoff); err != nil {
			s.Log.Warn("matrix: auto-delete", "room", c.room, "err", err)
			continue
		}
		if _, err := s.DB.Exec(ctx, `UPDATE im_autodelete SET swept = $2 WHERE room_id = $1`, c.room, cutoff); err != nil {
			return err
		}
	}
	return nil
}

// expireRoom redacts the room's messages sent in [from, to].
func (s *Service) expireRoom(ctx context.Context, room, userID string, from, to time.Time) error {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	lo, hi := from.UnixMilli(), to.UnixMilli()
	page := ""
	for {
		evs, end, err := s.M.MessagesOf(ctx, tok, room, page, 100, `["m.room.message","m.room.encrypted","org.matrix.msc3381.poll.start","m.poll.start"]`)
		if err != nil {
			return forbidden(err)
		}
		for _, ev := range evs {
			if ev.TS < lo {
				return nil
			}
			if ev.TS > hi || len(ev.Content) == 0 { // not yet, or deleted already
				continue
			}
			if err := s.redact(ctx, tok, room, ev); err != nil {
				return err
			}
		}
		if len(evs) == 0 || end == "" || end == page {
			return nil
		}
		page = end
	}
}

// redact deletes one message as tok's owner, or as its author when they may not, or else as go24 (a collab's chat,
// where go24 is the admin).
func (s *Service) redact(ctx context.Context, tok, room string, ev matrix.Event) error {
	err := s.M.RedactAs(ctx, tok, room, ev.EventID)
	if err == nil {
		return nil
	}
	var author string
	if s.DB.QueryRow(ctx, `SELECT user_id FROM matrix_users WHERE mxid = $1`, ev.Sender).Scan(&author) != nil {
		return err
	}
	if _, atok, aerr := s.token(ctx, author); aerr == nil && s.M.RedactAs(ctx, atok, room, ev.EventID) == nil {
		return nil
	}
	if own, oerr := s.ownRoom(ctx, room); oerr != nil || !own {
		return err
	}
	return s.M.Redact(ctx, room, ev.EventID, "")
}
