package matrixsync

import (
	"context"
	"sort"
	"strings"
	"time"

	"go24/internal/i18n"
	"go24/internal/matrix"
)

// Read receipts under one's own messages (✓ — sent, ✓✓ — read, with who has read it) and «печатает…».

// markReadBy names, under each of the employee's own messages, the others who have read it: those whose receipt
// is at the message or after it (by its place on the page, or by time when the receipt is on an event not shown).
func (s *Service) markReadBy(ctx context.Context, tok, me, room string, msgs []Message) {
	rs, err := s.M.Receipts(ctx, tok, room)
	if err != nil {
		s.Log.Warn("matrix: receipts", "room", room, "err", err)
		return
	}
	idx := map[string]int{}
	for i, m := range msgs {
		idx[m.ID] = i
	}
	var mxids []string
	for _, r := range rs {
		mxids = append(mxids, r.User)
	}
	who, _ := s.people(ctx, mxids)
	for _, r := range rs {
		if r.User == me || r.User == s.M.UserID("go24") {
			continue
		}
		name := personName(r.User, who)
		at, onPage := idx[r.Event]
		for i := range msgs {
			if !msgs[i].Mine || msgs[i].Event != "" {
				continue
			}
			if (onPage && i <= at) || (!onPage && !msgs[i].At.After(time.UnixMilli(r.TS))) {
				msgs[i].ReadBy = append(msgs[i].ReadBy, name)
			}
		}
	}
	for i := range msgs {
		sort.Strings(msgs[i].ReadBy)
	}
}

func personName(mxid string, who map[string]imPerson) string {
	if p, ok := who[mxid]; ok {
		return p.Name
	}
	return strings.TrimPrefix(strings.SplitN(mxid, ":", 2)[0], "@")
}

// Typing tells the chat the employee is typing (on) or has stopped.
func (s *Service) Typing(ctx context.Context, userID, room string, on bool) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	return forbidden(s.M.Typing(ctx, tok, room, me, on))
}

// liveEphemeral turns a chat's receipts and typing into live changes: "read" (ID — up to which event, Body — who,
// At — when) and "typing" (Body — who is typing now, "" — nobody). The employee's own are left out.
func (s *Service) liveEphemeral(ctx context.Context, me, room string, evs []matrix.Event) []Message {
	var out []Message
	for _, ev := range evs {
		switch ev.Type {
		case "m.receipt":
			for _, r := range matrix.ReceiptsOf(ev) {
				if r.User == me || r.User == s.M.UserID("go24") {
					continue
				}
				who, _ := s.people(ctx, []string{r.User})
				out = append(out, Message{RoomID: room, ID: r.Event, Change: "read", Body: personName(r.User, who), At: time.UnixMilli(r.TS)})
			}
		case "m.typing":
			ids, _ := ev.Content["user_ids"].([]any)
			var mxids []string
			for _, id := range ids {
				if mx, _ := id.(string); mx != "" && mx != me {
					mxids = append(mxids, mx)
				}
			}
			who, _ := s.people(ctx, mxids)
			var names []string
			for _, mx := range mxids {
				names = append(names, personName(mx, who))
			}
			sort.Strings(names)
			out = append(out, Message{RoomID: room, Change: "typing", Body: strings.Join(names, ", ")})
		}
	}
	return out
}

// hiddenType: the room's account data of a chat hidden from the list — {"ts": when, in ms}; a newer message
// brings the chat back; draftType — of an unsent text, {"text": …}.
const (
	hiddenType = "go24.hidden"
	draftType  = "go24.draft"
)

// RoomMark changes the employee's own mark of a chat in the list: "favorite" (pinned at the top), "mute" (no
// notifications), "unread" (marked unread until opened), "hidden" (out of the list until a new message); on — put,
// otherwise taken off.
func (s *Service) RoomMark(ctx context.Context, userID, room, mark string, on bool) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	if _, err := s.M.Members(ctx, room, tok); err != nil { // only one's own chats
		return forbidden(err)
	}
	switch mark {
	case "favorite":
		err = s.M.SetTag(ctx, tok, me, room, "m.favourite", on)
	case "mute":
		err = s.M.Mute(ctx, tok, room, on)
	case "unread":
		err = s.M.SetRoomData(ctx, tok, me, room, "m.marked_unread", map[string]bool{"unread": on})
	case "hidden":
		var ts int64 // the newest event's time, on the Matrix server's clock: a message after it brings the chat back
		if on {
			ts = time.Now().UnixMilli()
			if evs, _, err := s.M.Messages(ctx, tok, room, "", 1); err == nil && len(evs) > 0 {
				ts = evs[0].TS
			}
		}
		err = s.M.SetRoomData(ctx, tok, me, room, hiddenType, map[string]int64{"ts": ts})
	default:
		return i18n.E("err.forbidden")
	}
	return forbidden(err)
}

// maxDraft is how much of an unsent text the chat keeps (runes).
const maxDraft = 10000

// draft is the content of go24.draft: the unsent text of the chat's field and of each of its threads (by root).
type draft struct {
	Text    string            `json:"text"`
	Threads map[string]string `json:"threads,omitempty"`
}

// SetDraft keeps the employee's unsent text of the chat ("" — none), or of its thread (root): their other devices
// and the chat list show it.
func (s *Service) SetDraft(ctx context.Context, userID, room, thread, text string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	if r := []rune(text); len(r) > maxDraft {
		text = string(r[:maxDraft])
	}
	if strings.TrimSpace(text) == "" {
		text = ""
	}
	s.draftMu.Lock() // the field and a thread are kept in one piece of account data: one change of it at a time
	defer s.draftMu.Unlock()
	var d draft
	if err := s.M.RoomData(ctx, tok, me, room, draftType, &d); err != nil && !matrix.IsCode(err, "M_NOT_FOUND") {
		return forbidden(err)
	}
	if thread == "" {
		d.Text = text
	} else if text != "" {
		if d.Threads == nil {
			d.Threads = map[string]string{}
		}
		d.Threads[thread] = text
	} else {
		delete(d.Threads, thread)
	}
	return forbidden(s.M.SetRoomData(ctx, tok, me, room, draftType, d))
}

// ThreadDraft is the employee's unsent text of the chat's thread (root).
func (s *Service) ThreadDraft(ctx context.Context, userID, room, thread string) (string, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return "", err
	}
	var d draft
	if err := s.M.RoomData(ctx, tok, me, room, draftType, &d); err != nil && !matrix.IsCode(err, "M_NOT_FOUND") {
		return "", forbidden(err)
	}
	return d.Threads[thread], nil
}
