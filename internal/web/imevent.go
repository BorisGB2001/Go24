package web

import (
	"net/http"
	"strings"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/tasks"
)

// ---------- a meeting made of a chat message («Создать встречу» in the message's menu) ----------

// eventFromMessage fills the new meeting's form: the title and the description as a task of the message has them,
// the chat's people invited, the chat's project's (a collab's) calendar when the chat has one.
func (s *Server) eventFromMessage(r *http.Request, sess auth.Session, room, event string, data map[string]any) {
	ctx := r.Context()
	m, chat, projectID, err := s.Matrix.One(ctx, sess.UserID, room, event)
	if err != nil {
		return
	}
	e := data["E"].(calendar.Event)
	e.Title, e.Description = s.messageTask(r, m, chat, s.imMessageURL(room, event))
	if projectID != "" {
		for _, p := range data["Projects"].([]tasks.Project) {
			if p.ID == projectID {
				e.ProjectID = p.ID
			}
		}
	}
	var invited []tasks.User
	if people, err := s.Matrix.ChatMembers(ctx, sess.UserID, room); err == nil {
		for _, p := range people {
			if !p.Me && p.ID != "" {
				invited = append(invited, tasks.User{ID: p.ID, Name: p.Name})
			}
		}
	}
	data["E"], data["Invited"], data["IM"] = e, invited, &imSource{Room: room, Event: event, Sender: m.SenderName}
}

// eventMessageDone answers the message in its chat with the new meeting (as the person who made it).
func (s *Server) eventMessageDone(r *http.Request, sess auth.Session, room, event, id string) {
	ctx := r.Context()
	m, _, _, err := s.Matrix.One(ctx, sess.UserID, room, event)
	if err != nil {
		s.Log.Warn("meeting of a message", "event", id, "err", err)
		return
	}
	e, err := s.Calendar.Get(ctx, sess.UserID, id)
	if err != nil {
		s.Log.Warn("meeting of a message", "event", id, "err", err)
		return
	}
	link := strings.TrimRight(s.BaseURL, "/") + "/calendar/" + id
	when := e.Start.Local().Format(s.tr(r, "format.datetime"))
	if e.AllDay {
		when = e.Start.Local().Format(s.tr(r, "format.date"))
	}
	if _, err := s.Matrix.Reply(ctx, sess.UserID, room, s.tr(r, "im.event_made", e.Title, when, link), m.ThreadRoot, event); err != nil {
		s.Log.Warn("meeting of a message: the answer in the chat", "event", id, "err", err)
	}
}
