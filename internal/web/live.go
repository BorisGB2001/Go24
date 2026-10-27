package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// taskLive streams new messages of the task chat to an open card (Server-Sent Events). Each event carries the
// rendered message; the page adds it unless it is already there (the author's own post).
func (s *Server) taskLive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Live == nil {
		http.NotFound(w, r)
		return
	}
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err == nil {
		err = s.Tasks.CheckVisible(r.Context(), sess.UserID, id)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	events, stop := s.Live.Subscribe(id)
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // proxies must not buffer the stream
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	page := s.pages[s.lang(r)]["task"]
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case cid := <-events:
			// visibility may have changed since the page opened
			if s.Tasks.CheckVisible(r.Context(), sess.UserID, id) != nil {
				return
			}
			c, err := s.Tasks.CommentByID(r.Context(), id, cid)
			if errors.Is(err, tasks.ErrNotFound) { // deleted: the page takes it away
				data, _ := json.Marshal(map[string]string{"id": cid})
				fmt.Fprintf(w, "event: gone\ndata: %s\n\n", data)
				flusher.Flush()
				continue
			}
			if err != nil {
				continue
			}
			one := []tasks.Comment{c}
			if err := s.Tasks.FillReactions(r.Context(), sess.UserID, one); err == nil {
				c = one[0]
			}
			var buf bytes.Buffer
			if err := page.ExecuteTemplate(&buf, "chatmsg", s.chatItemOf(r, sess, &c, r.PathValue("key"))); err != nil {
				s.Log.Error("live render", "err", err)
				continue
			}
			data, _ := json.Marshal(map[string]string{"id": c.ID, "html": buf.String()})
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// commentReact puts or takes back the person's reaction on a task chat message; the answer is the message again.
func (s *Server) commentReact(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	cid, key := r.PathValue("id"), r.FormValue("emoji")
	if err == nil {
		if s.Matrix != nil {
			err = s.Matrix.ReactComment(r.Context(), sess.UserID, id, cid, key)
		} else {
			var on bool
			if on, err = s.Tasks.HasReaction(r.Context(), sess.UserID, cid, key); err == nil {
				_, err = s.Tasks.SetReaction(r.Context(), sess.UserID, id, cid, key, !on, "")
			}
		}
	}
	var c tasks.Comment
	if err == nil {
		c, err = s.Tasks.CommentByID(r.Context(), id, cid)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	one := []tasks.Comment{c}
	if err := s.Tasks.FillReactions(r.Context(), sess.UserID, one); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["task"].ExecuteTemplate(&buf, "chatmsg", s.chatItemOf(r, sess, &one[0], r.PathValue("key"))); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": c.ID, "html": buf.String()})
}
