package web

import (
	"net/http"
	"strings"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// ---------- a meeting made of a chat message («Создать встречу» in the message's menu) ----------

// eventFromMessage fills the new meeting's form: the title and the description as a task of the message has them,
// the chat's people invited, the chat's project's (a collab's) calendar when the chat has one. With no message (event
// "") — a meeting of the chat itself, from its panel's «Встречи».
func (s *Server) eventFromMessage(r *http.Request, sess auth.Session, room, event string, data map[string]any) {
	ctx := r.Context()
	e := data["E"].(calendar.Event)
	var src *imSource
	var projectID string
	if event == "" {
		chat, project, err := s.Matrix.ChatInfo(ctx, sess.UserID, room)
		if err != nil {
			return
		}
		src, projectID = &imSource{Room: room, Chat: chat}, project
	} else {
		m, chat, project, err := s.Matrix.One(ctx, sess.UserID, room, event)
		if err != nil {
			return
		}
		src, projectID = &imSource{Room: room, Event: event, Sender: m.SenderName}, project
		e.Title, e.Description = s.messageTask(r, m, chat, s.imMessageURL(room, event))
	}
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
	data["E"], data["Invited"], data["IM"] = e, invited, src
}

// eventMessageDone answers the message in its chat with the new meeting (as the person who made it) and ties the
// meeting to the chat (its panel's «Встречи»). With no message — a line in the chat.
func (s *Server) eventMessageDone(r *http.Request, sess auth.Session, room, event, id string) {
	ctx := r.Context()
	var m matrixsync.Message
	var err error
	if event != "" {
		if m, _, _, err = s.Matrix.One(ctx, sess.UserID, room, event); err != nil {
			s.Log.Warn("meeting of a message", "event", id, "err", err)
			return
		}
	}
	e, err := s.Calendar.Get(ctx, sess.UserID, id)
	if err != nil {
		s.Log.Warn("meeting of a message", "event", id, "err", err)
		return
	}
	if err := s.Matrix.TieItem(ctx, sess.UserID, room, "event", id, event); err != nil {
		s.Log.Warn("meeting of a chat", "event", id, "err", err)
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
