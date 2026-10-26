package web

import (
	"net/http"
	"strings"

	"go24/internal/auth"
)

// «Задача» and «Событие» of the feed: a task or a meeting made of a post («Создать задачу»,
// «Создать встречу» of a post, ?feed=ID), a task made of a comment (?feed_comment=ID) or of what is typed in the
// feed's form (?title=, ?description=).

// feedSource is the title and the text of a new task or meeting: the post's (who wrote it, when; its text; the link
// to it) or the ones typed; ok is false when there is neither.
func (s *Server) feedSource(r *http.Request, sess auth.Session) (title, text string, ok bool) {
	q := r.URL.Query()
	if id := q.Get("feed"); id != "" {
		it, err := s.Feed.Item(r.Context(), sess.UserID, id)
		if err != nil || it.Post == nil {
			return "", "", false
		}
		p := it.Post
		title = p.Title
		if title == "" {
			for _, line := range strings.Split(p.Body, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					title = clipText(line, 200)
					break
				}
			}
		}
		head := s.tr(r, "feed.task_head", p.AuthorName, p.CreatedAt.Local().Format(s.tr(r, "format.datetime")))
		link := strings.TrimRight(s.BaseURL, "/") + "/feed#post-" + p.ID
		return title, clipText(head+"\n\n"+p.Body+"\n\n"+link, 20000), true
	}
	if id := q.Get("feed_comment"); id != "" { // «Создать задачу» of a comment
		c, err := s.Feed.Comment(r.Context(), sess.UserID, id)
		if err != nil {
			return "", "", false
		}
		for _, line := range strings.Split(c.Body, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				title = clipText(line, 200)
				break
			}
		}
		head := s.tr(r, "feed.task_head_comment", c.AuthorName, c.CreatedAt.Local().Format(s.tr(r, "format.datetime")))
		link := strings.TrimRight(s.BaseURL, "/") + "/feed?post=" + c.PostID + "#comment-" + c.ID
		return title, clipText(head+"\n\n"+c.Body+"\n\n"+link, 20000), true
	}
	title, text = strings.TrimSpace(q.Get("title")), strings.TrimSpace(q.Get("description"))
	return clipText(title, 300), clipText(text, 20000), title != "" || text != ""
}
