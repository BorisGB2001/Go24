package web

import (
	"net/http"
	"strings"

	"go24/internal/auth"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- a feed post made of a chat message («Создать пост в ленте» in the message's menu) ----------

// postFromMessage fills the feed's new post: the message with who wrote it, when, where and a link to it, to the
// chat's project (a collab's) when the chat has one.
func (s *Server) postFromMessage(r *http.Request, sess auth.Session, room, event string, data map[string]any) {
	m, chat, projectID, err := s.Matrix.One(r.Context(), sess.UserID, room, event)
	if err != nil {
		return
	}
	_, body := s.messageTask(r, m, chat, s.imMessageURL(room, event))
	data["IM"], data["IMBody"] = &imSource{Room: room, Event: event, Sender: m.SenderName}, body
	if m.File != nil {
		data["IMFile"] = m.File.Name
	}
	for _, p := range data["Projects"].([]any) { // only one the person may post to
		if p, ok := p.(tasks.Project); ok && projectID != "" && p.ID == projectID {
			data["IMProject"] = projectID
		}
	}
}

// postMessageDone copies the message's file to the new post and answers the message in its chat with the post (as
// the person who made it).
func (s *Server) postMessageDone(r *http.Request, sess auth.Session, room, event, id string) {
	ctx := r.Context()
	m, _, _, err := s.Matrix.One(ctx, sess.UserID, room, event)
	if err != nil {
		s.Log.Warn("post of a message", "post", id, "err", err)
		return
	}
	if m.File != nil {
		if _, err := s.messageFileTo(r, sess, room, event, storages.NewFile{PostID: id}); err != nil {
			s.Log.Warn("message file to a post", "post", id, "err", err)
		}
	}
	link := strings.TrimRight(s.BaseURL, "/") + "/feed#post-" + id
	if _, err := s.Matrix.Reply(ctx, sess.UserID, room, s.tr(r, "im.post_made", link), m.ThreadRoot, event); err != nil {
		s.Log.Warn("post of a message: the answer in the chat", "post", id, "err", err)
	}
}
