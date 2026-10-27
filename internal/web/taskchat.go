package web

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// The task chat's message menu: «Ответить» (handled by the chat form: reply_to), «Изменить» and
// «Удалить» one's own message (an administrator deletes any), «Создать подзадачу» of the message.

// commentEdit changes the text of the person's message; the answer is the message again.
func (s *Server) commentEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	var c tasks.Comment
	if err == nil {
		c, err = s.editComment(r, sess, id, r.PathValue("id"), r.FormValue("body"))
	}
	if err == nil {
		err = s.writeChatMsg(w, r, sess, c)
	}
	if err != nil {
		s.jsonErr(w, r, err)
	}
}

// commentDelete removes the message from the chat (and the messenger).
func (s *Server) commentDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err == nil {
		err = s.deleteComment(r, sess, id, r.PathValue("id"))
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if !wantsJSON(r) {
		http.Redirect(w, r, "/t/"+url.PathEscape(r.PathValue("key"))+"#comments", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "deleted": true})
}

func (s *Server) editComment(r *http.Request, sess auth.Session, taskID, id, body string) (tasks.Comment, error) {
	body = strings.TrimSpace(body)
	if s.Matrix == nil || body == "" {
		return s.Tasks.EditComment(r.Context(), sess.UserID, taskID, id, body)
	}
	c, err := s.Matrix.EditTaskComment(r.Context(), sess.UserID, taskID, id, body)
	return c, s.messengerErr(err)
}

func (s *Server) deleteComment(r *http.Request, sess auth.Session, taskID, id string) error {
	if s.Matrix == nil {
		return s.Tasks.DeleteComment(r.Context(), sess.UserID, taskID, id)
	}
	return s.messengerErr(s.Matrix.DeleteTaskComment(r.Context(), sess.UserID, taskID, id))
}

// messengerErr hides a failure of the messenger behind «нет связи с мессенджером» (the person keeps their text);
// rights and input errors pass as they are.
func (s *Server) messengerErr(err error) error {
	var ie *i18n.Error
	if err != nil && !errors.As(err, &ie) {
		s.Log.Error("task chat in matrix", "err", err)
		return i18n.E("err.messenger")
	}
	return err
}

// writeChatMsg answers with the rendered message, as the chat shows it to the person.
func (s *Server) writeChatMsg(w http.ResponseWriter, r *http.Request, sess auth.Session, c tasks.Comment) error {
	one := []tasks.Comment{c}
	if err := s.Tasks.FillReactions(r.Context(), sess.UserID, one); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["task"].ExecuteTemplate(&buf, "chatmsg", s.chatItemOf(r, sess, &one[0], r.PathValue("key"))); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": c.ID, "html": buf.String()})
	return nil
}

// chatItemOf is a message as the person sees it in the chat.
func (s *Server) chatItemOf(r *http.Request, sess auth.Session, c *tasks.Comment, key string) chatItem {
	return chatItem{Comment: c, Mine: c.AuthorID == sess.UserID, At: c.CreatedAt, Key: key}
}

// taskFromComment fills the new subtask's form with a message of the parent task's chat: its first line is the
// title, the message (who wrote it and when) and a link to the task the description.
func (s *Server) taskFromComment(r *http.Request, sess auth.Session, parent, commentID string, f *taskForm) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), parent)
	if err == nil {
		err = s.Tasks.CheckVisible(r.Context(), sess.UserID, id)
	}
	if err != nil {
		return
	}
	c, err := s.Tasks.CommentByID(r.Context(), id, commentID)
	if err != nil {
		return
	}
	link := strings.TrimRight(s.BaseURL, "/") + "/t/" + url.PathEscape(parent) + "#m-" + c.ID
	f.Title, f.Description = s.messageTask(r, matrixsync.Message{Body: c.Body, SenderName: c.AuthorName, At: c.CreatedAt}, "", link)
}
