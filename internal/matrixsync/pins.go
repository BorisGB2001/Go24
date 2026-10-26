package matrixsync

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/matrix"
)

// Pinned messages (m.room.pinned_events, the bar under a chat's header) and the search of messages.

// maxPins: the bar keeps that many messages, the oldest pins go.
const maxPins = 20

// pinned gives the IDs of the chat's pinned messages, in the order they were pinned.
func (s *Service) pinned(ctx context.Context, tok, room string) []string {
	var st struct {
		Pinned []string `json:"pinned"`
	}
	if err := s.M.StateAs(ctx, tok, room, "m.room.pinned_events", "", &st); err != nil && !matrix.IsCode(err, "M_NOT_FOUND") {
		s.Log.Warn("matrix: pinned", "room", room, "err", err)
	}
	return st.Pinned
}

// markPinned flags the pinned ones among the messages.
func (s *Service) markPinned(ctx context.Context, tok, room string, msgs []Message) {
	ids := s.pinned(ctx, tok, room)
	for i := range msgs {
		msgs[i].Pinned = slices.Contains(ids, msgs[i].ID)
	}
}

// Pins are the chat's pinned messages, the latest pinned first; the ones taken back are left out.
func (s *Service) Pins(ctx context.Context, userID, room string) ([]Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, err
	}
	ids := s.pinned(ctx, tok, room)
	var evs []matrix.Event
	for i := len(ids) - 1; i >= 0; i-- {
		if ev, err := s.M.EventAs(ctx, tok, room, ids[i]); err == nil {
			evs = append(evs, ev)
		}
	}
	out := s.convert(ctx, evs, room, me, false) // it turns them over (as /messages pages go back): the latest pin is first again
	slices.Reverse(out)
	for i := range out {
		out[i].Pinned = true
	}
	return out, nil
}

// Pin pins the message of the chat (on) or takes the pin back. Anyone in the chat may: in go24's own chats (the
// people there have no rights over the chat in Matrix) the bot puts it.
func (s *Service) Pin(ctx context.Context, userID, room, eventID string, on bool) error {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	if _, err := s.M.EventAs(ctx, tok, room, eventID); err != nil {
		return forbidden(err)
	}
	ids := slices.DeleteFunc(s.pinned(ctx, tok, room), func(id string) bool { return id == eventID })
	if on {
		ids = append(ids, eventID)
	}
	if len(ids) > maxPins {
		ids = ids[len(ids)-maxPins:]
	}
	content := map[string]any{"pinned": append([]string{}, ids...)}
	err = s.M.SetStateAs(ctx, tok, room, "m.room.pinned_events", "", content)
	if !matrix.IsCode(err, "M_FORBIDDEN") {
		return err
	}
	var kind string
	if qerr := s.DB.QueryRow(ctx, `SELECT kind FROM matrix_rooms WHERE room_id = $1`, room).Scan(&kind); errors.Is(qerr, pgx.ErrNoRows) {
		return i18n.E("err.forbidden") // someone else's group chat: its owner decides who pins
	} else if qerr != nil {
		return qerr
	}
	return s.M.SetState(ctx, room, "m.room.pinned_events", "", content)
}

// Search finds messages by their words in the chat (room) or in all the employee's chats (room ""), newest first.
func (s *Service) Search(ctx context.Context, userID, q, room string) ([]Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, err
	}
	q = strings.TrimSpace(q)
	if utf8.RuneCountInString(q) < 2 {
		return nil, nil
	}
	var rooms []string
	if room != "" {
		rooms = []string{room}
	}
	evs, _, err := s.M.Search(ctx, tok, q, rooms, "")
	if err != nil {
		return nil, forbidden(err)
	}
	var mxids []string
	for _, ev := range evs {
		mxids = append(mxids, ev.Sender)
	}
	who, _ := s.people(ctx, mxids)
	var out []Message
	for _, ev := range evs {
		if m, ok := s.message(ev, ev.RoomID, me, who); ok && !m.Notice {
			out = append(out, m)
		}
	}
	return out, nil
}

// Around is the page of a chat around one of its messages (a search result, a pin): oldest first, and the token
// for the page before it.
func (s *Service) Around(ctx context.Context, userID, room, eventID string) ([]Message, string, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	a, err := s.M.Context(ctx, tok, room, eventID, 30)
	if err != nil {
		return nil, "", forbidden(err)
	}
	evs := slices.Clone(a.After)
	slices.Reverse(evs)
	evs = append(append(evs, a.Event), a.Before...) // newest first, as convert wants
	msgs := s.convert(ctx, append(evs, s.recentReactions(ctx, tok, room)...), room, me, true)
	s.markPinned(ctx, tok, room, msgs)
	s.markReadBy(ctx, tok, me, room, msgs)
	s.markSaved(ctx, userID, msgs)
	s.tallyPolls(ctx, tok, room, me, msgs)
	s.markNewReplies(ctx, tok, room, msgs)
	return msgs, a.Start, nil
}

// History is the whole chat to download — the latest limit messages, oldest first, replies of threads too — and
// the chat's name.
func (s *Service) History(ctx context.Context, userID, roomID string, limit int) ([]Message, string, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	var evs []matrix.Event
	from := ""
	for len(evs) < limit {
		page, end, err := s.M.Messages(ctx, tok, roomID, from, 500)
		if err != nil {
			return nil, "", forbidden(err)
		}
		evs = append(evs, page...)
		if len(page) == 0 || end == "" || end == from {
			break
		}
		from = end
	}
	var name struct {
		Name string `json:"name"`
	}
	_ = s.M.StateAs(ctx, tok, roomID, "m.room.name", "", &name)
	return s.convert(ctx, evs, roomID, me, false), name.Name, nil
}
