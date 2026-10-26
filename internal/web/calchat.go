package web

import (
	"context"
	"net/http"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
)

// «Чат встречи»: the messenger's group chat of the meeting's organizer and people, made by the
// organizer on first use; those invited later and those who join an open event come into it too.

// groupChats are the group chats of the messenger (s.Matrix; Chats in tests).
type groupChats interface {
	CreateGroup(ctx context.Context, userID, name string, ids []string, channel bool) (string, error)
	AddToGroup(ctx context.Context, userID, room string, ids []string) error
	SetGroupTopic(ctx context.Context, userID, room, topic string) error
}

func (s *Server) groupChats() groupChats {
	if s.Chats != nil {
		return s.Chats
	}
	if s.Matrix != nil {
		return s.Matrix
	}
	return nil
}

// chatPeople are the meeting's people who have not declined, but its organizer.
func chatPeople(e calendar.Event) []string {
	var ids []string
	for _, a := range e.Attendees {
		if a.Status != "declined" && a.ID != e.OwnerID {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

// mayChat: the viewer is the meeting's organizer or one of its people who has not declined.
func mayChat(e calendar.Event, viewer string) bool {
	if e.Kind != "event" || e.Hidden || e.ID == "" {
		return false
	}
	return e.OwnerID == viewer || e.MyStatus != "" && e.MyStatus != "declined"
}

// eventChat opens the meeting's chat (POST /calendar/{id}/chat), making it first.
func (s *Server) eventChat(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	room, err := s.meetingChat(r, sess.UserID, id)
	if err != nil {
		s.taskErr(w, r, err, "/calendar/"+id)
		return
	}
	http.Redirect(w, r, imRoomURL(room), http.StatusSeeOther)
}

// meetingChat is the room of the meeting's chat for the viewer: made by the organizer when there is none, the viewer
// added when they are not in it yet.
func (s *Server) meetingChat(r *http.Request, viewer, id string) (string, error) {
	chats := s.groupChats()
	if chats == nil {
		return "", i18n.E("err.not_found")
	}
	ctx := r.Context()
	e, err := s.Calendar.Get(ctx, viewer, id)
	if err != nil {
		return "", err
	}
	if !mayChat(e, viewer) {
		return "", calendar.ErrForbidden
	}
	if e.ChatRoom != "" {
		if viewer != e.OwnerID {
			err = chats.AddToGroup(ctx, e.OwnerID, e.ChatRoom, []string{viewer})
		}
		return e.ChatRoom, err
	}
	people := chatPeople(e)
	if len(people) == 0 {
		return "", i18n.E("err.event_chat_people")
	}
	room, err := chats.CreateGroup(ctx, e.OwnerID, strings.TrimSpace(e.Title), people, false)
	if err != nil {
		return "", err
	}
	if kept, err := s.Calendar.SetChat(ctx, e.ID, room); err != nil || kept != room { // made twice at once: the first one
		return kept, err
	}
	topic := s.tr(r, "calendar.chat_topic", e.Start.Format("02.01.2006 15:04"), strings.TrimRight(s.BaseURL, "/")+"/calendar/"+e.ID)
	if err := chats.SetGroupTopic(ctx, e.OwnerID, room, topic); err != nil {
		s.Log.Warn("event chat topic", "event", e.ID, "err", err)
	}
	return room, nil
}

// meetingChatSync brings the meeting's people who are not in its chat yet into it (after a change, a join).
func (s *Server) meetingChatSync(ctx context.Context, viewer, id string) {
	chats := s.groupChats()
	if chats == nil || id == "" {
		return
	}
	e, err := s.Calendar.Get(ctx, viewer, id)
	if err != nil || e.ChatRoom == "" {
		return
	}
	if people := chatPeople(e); len(people) > 0 {
		if err := chats.AddToGroup(ctx, e.OwnerID, e.ChatRoom, people); err != nil {
			s.Log.Warn("event chat people", "event", id, "err", err)
		}
	}
}

// apiCalChat: the meeting's chat (made first) — {"room": …, "url": …}.
func (s *Server) apiCalChat(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	room, err := s.meetingChat(r, p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"room": room, "url": strings.TrimRight(s.BaseURL, "/") + imRoomURL(room)})
	return nil
}
