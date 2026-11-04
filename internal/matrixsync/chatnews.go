package matrixsync

import (
	"context"
)

// Chat messages for the outside messengers (notifyout: Telegram and the others): the new messages of a person's
// chats since a sync position of their own pushes them to the phone of someone not at the portal.

// ChatNew is a new message of someone else in one of the person's chats.
type ChatNew struct {
	Room, Event, Root string // Root: the thread's root, for a reply in a thread
	Chat              string // the chat's name ("" — a private chat: the sender is the chat)
	Sender, Text      string
}

// ChatNews: the messages since the position (from "" — none, only the current position) that are still unread
// for the person — muted chats and threads count nothing, so they are left out; the next position.
func (s *Service) ChatNews(ctx context.Context, userID, since string) ([]ChatNew, string, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if since == "" {
		sy, err := s.M.SyncAs(ctx, tok, "", 0, 0)
		if err != nil {
			return nil, "", err
		}
		return nil, sy.NextBatch, nil
	}
	sy, err := s.M.SyncAs(ctx, tok, since, 0, 20)
	if err != nil {
		return nil, "", err
	}
	var out []ChatNew
	for id, r := range sy.Rooms.Join {
		evs := r.Timeline.Events
		for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 { // convert wants newest first
			evs[i], evs[j] = evs[j], evs[i]
		}
		name := ""
		for _, m := range s.convert(ctx, evs, id, me, false) {
			if m.Mine || m.Notice || m.Event != "" || m.Call != nil || m.Change != "" {
				continue
			}
			if m.ThreadRoot == "" && r.Unread.Notifications == 0 || m.ThreadRoot != "" && r.Threads[m.ThreadRoot].Notifications == 0 {
				continue // read already, or the chat is muted
			}
			if name == "" {
				name = s.chatName(ctx, tok, id)
			}
			text := m.Body
			if m.File != nil {
				text = "📎 " + m.File.Name
			}
			out = append(out, ChatNew{Room: id, Event: m.ID, Root: m.ThreadRoot, Chat: name, Sender: m.SenderName, Text: text})
		}
	}
	return out, sy.NextBatch, nil
}

// chatName: the room's own name; "" for a private chat, which has none.
func (s *Service) chatName(ctx context.Context, tok, roomID string) string {
	var st struct {
		Name string `json:"name"`
	}
	if s.M.StateAs(ctx, tok, roomID, "m.room.name", "", &st) != nil {
		return ""
	}
	return st.Name
}
