package matrixsync

import (
	"context"
	"errors"
	"time"

	"go24/internal/feed"
	"go24/internal/tasks"
)

// Matrix is where go24's conversations are kept: a task chat message, a feed post, a comment, a like is first
// written to Matrix (as its author) and only then indexed in go24's database, which serves the pages and can be
// rebuilt from Matrix. When the messenger is unreachable the write fails and the person keeps their text.

// send posts content to a room as the go24 user (the bot posts for people without a Matrix account, with their name).
func (s *Service) sendAs(ctx context.Context, userID, name, room string, content map[string]any) (string, error) {
	token, err := s.as(ctx, userID, room)
	if err != nil {
		return "", err
	}
	if token == "" {
		if b, ok := content["body"].(string); ok {
			content["body"] = name + ": " + b
		}
	}
	return s.M.Send(ctx, room, content, token)
}

func threadRel(root string) map[string]any {
	return map[string]any{"rel_type": "m.thread", "event_id": root, "is_falling_back": true,
		"m.in_reply_to": map[string]string{"event_id": root}}
}

// PostTaskComment writes a task chat message to Matrix, then indexes it in go24.
func (s *Service) PostTaskComment(ctx context.Context, userID, taskID, body string) (tasks.Comment, error) {
	return s.PostTaskReply(ctx, userID, taskID, "", body)
}

// PostTaskReply is PostTaskComment answering another message (replyTo, "" = none): in the messenger it is a
// reply to that message within the task's thread.
func (s *Service) PostTaskReply(ctx context.Context, userID, taskID, replyTo, body string) (tasks.Comment, error) {
	if err := s.Tasks.CheckVisible(ctx, userID, taskID); err != nil {
		return tasks.Comment{}, err
	}
	room, root, err := s.target(ctx, taskID)
	if errors.Is(err, errRoomNotReady) { // a brand-new project: make its room now and try again
		if err = s.Reconcile(ctx); err == nil {
			room, root, err = s.target(ctx, taskID)
		}
	}
	if err != nil {
		return tasks.Comment{}, err
	}
	var name string
	_ = s.DB.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&name)
	content := map[string]any{"msgtype": "m.text", "body": body, "go24.comment": true}
	if root != "" {
		content["m.relates_to"] = threadRel(root)
	}
	if ev := s.commentEvent(ctx, taskID, replyTo); ev != "" {
		rel, _ := content["m.relates_to"].(map[string]any)
		if rel == nil {
			rel = map[string]any{}
			content["m.relates_to"] = rel
		}
		rel["m.in_reply_to"] = map[string]string{"event_id": ev}
		rel["is_falling_back"] = false
	}
	if err := s.mention(ctx, content, body, ""); err != nil {
		return tasks.Comment{}, err
	}
	ev, err := s.sendAs(ctx, userID, name, room, content)
	if err != nil {
		return tasks.Comment{}, err
	}
	c, err := s.Tasks.PostReply(ctx, userID, taskID, replyTo, body)
	if err != nil {
		return c, err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO matrix_events (event_id, comment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, ev, c.ID)
	return c, err
}

// backfill moves a task's earlier messages (written before it had a room or thread, or imported) into Matrix,
// oldest first, each with its original date.
func (s *Service) backfill(ctx context.Context, taskID, room, root string) error {
	rows, err := s.DB.Query(ctx, `SELECT c.id::text, c.author_id::text, u.name, c.body, c.created_at FROM task_comments c
		JOIN users u ON u.id = c.author_id
		WHERE c.task_id = $1 AND NOT EXISTS (SELECT 1 FROM matrix_events e WHERE e.comment_id = c.id) ORDER BY c.created_at`, taskID)
	if err != nil {
		return err
	}
	type old struct {
		ID, Author, Name, Body string
		At                     time.Time
	}
	var list []old
	for rows.Next() {
		var o old
		if err := rows.Scan(&o.ID, &o.Author, &o.Name, &o.Body, &o.At); err != nil {
			rows.Close()
			return err
		}
		list = append(list, o)
	}
	rows.Close()
	for _, o := range list {
		content := map[string]any{"msgtype": "m.text", "body": "🕓 " + o.At.Local().Format("02.01.2006 15:04") + "\n" + o.Body, "go24.comment": o.ID}
		if root != "" {
			content["m.relates_to"] = threadRel(root)
		}
		ev, err := s.sendAs(ctx, o.Author, o.Name, room, content)
		if err != nil {
			return err
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO matrix_events (event_id, comment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, ev, o.ID); err != nil {
			return err
		}
	}
	return nil
}

// ---------- feed: Matrix first ----------

// PublishPost writes a new post (already indexed by feed.CreatePost) to Matrix. The caller removes the index row
// when this fails.
func (s *Service) PublishPost(ctx context.Context, postID string) error {
	b, err := s.Feed.BridgeInfo(ctx, postID)
	if err != nil || b.Event != "" || b.Limited { // a post to chosen people is not for the company's room
		return err
	}
	room, err := s.postRoom(ctx, b.ProjectID)
	if err != nil {
		return err
	}
	if room == "" {
		return errNoRoom
	}
	var name string
	_ = s.DB.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, b.AuthorID).Scan(&name)
	plain, rich := postText(b)
	content := map[string]any{"msgtype": "m.text", "body": plain,
		"format": "org.matrix.custom.html", "formatted_body": rich, "go24.post": b.ID}
	if err := s.mention(ctx, content, plain, rich); err != nil {
		return err
	}
	ev, err := s.sendAs(ctx, b.AuthorID, name, room, content)
	if err != nil {
		return err
	}
	return s.Feed.SetPostEvent(ctx, b.ID, room, ev)
}

// PublishEdit sends the new text to Matrix first; the caller saves it in go24 only when this succeeds.
func (s *Service) PublishEdit(ctx context.Context, postID, title, body string) error {
	b, err := s.Feed.BridgeInfo(ctx, postID)
	if err != nil || b.Event == "" {
		return err
	}
	b.Title, b.Body = title, body
	token, err := s.as(ctx, b.AuthorID, b.Room)
	if err != nil {
		return err
	}
	plain, rich := postText(b)
	_, err = s.M.Send(ctx, b.Room, map[string]any{"msgtype": "m.text", "body": "* " + plain, "go24.post": b.ID,
		"m.new_content": map[string]any{"msgtype": "m.text", "body": plain, "format": "org.matrix.custom.html", "formatted_body": rich},
		"m.relates_to":  map[string]any{"rel_type": "m.replace", "event_id": b.Event}}, token)
	return err
}

// MovePost takes a readdressed post to its new room: its messages in the old room go, the post and its comments
// (each with its date) are written anew; a post to chosen people stays in go24 only.
func (s *Service) MovePost(ctx context.Context, postID string) error {
	room, events, err := s.Feed.Unbridge(ctx, postID)
	if err != nil {
		return err
	}
	for _, ev := range events {
		s.logErr("move", s.M.Redact(ctx, room, ev, "go24"))
	}
	if err := s.PublishPost(ctx, postID); err != nil {
		return err
	}
	b, err := s.Feed.BridgeInfo(ctx, postID)
	if err != nil || b.Event == "" {
		return err
	}
	comments, err := s.Feed.BridgeComments(ctx, postID)
	if err != nil {
		return err
	}
	for _, c := range comments {
		content := map[string]any{"msgtype": "m.text", "body": "🕓 " + c.CreatedAt.Local().Format("02.01.2006 15:04") + "\n" + c.Body,
			"go24.feedcomment": c.ID, "m.relates_to": threadRel(b.Event)}
		ev, err := s.sendAs(ctx, c.AuthorID, c.AuthorName, b.Room, content)
		if err != nil {
			return err
		}
		if err := s.Feed.SetCommentEvent(ctx, c.ID, ev); err != nil {
			return err
		}
	}
	return nil
}

// PublishDelete removes the post from Matrix first.
func (s *Service) PublishDelete(ctx context.Context, postID string) error {
	b, err := s.Feed.BridgeInfo(ctx, postID)
	if err != nil || b.Event == "" {
		return err
	}
	return s.M.Redact(ctx, b.Room, b.Event, "go24")
}

// PublishComment writes a comment (already indexed) into the post's thread.
func (s *Service) PublishComment(ctx context.Context, postID string, c feed.Comment) error {
	b, err := s.Feed.BridgeInfo(ctx, postID)
	if err != nil || b.Event == "" {
		return err
	}
	content := map[string]any{"msgtype": "m.text", "body": c.Body, "go24.feedcomment": c.ID, "m.relates_to": threadRel(b.Event)}
	if err := s.mention(ctx, content, c.Body, ""); err != nil {
		return err
	}
	ev, err := s.sendAs(ctx, c.AuthorID, c.AuthorName, b.Room, content)
	if err != nil {
		return err
	}
	return s.Feed.SetCommentEvent(ctx, c.ID, ev)
}

// PublishCommentEdit sends a comment's new text to its post's thread first; the caller saves it in go24 only when
// this succeeds.
func (s *Service) PublishCommentEdit(ctx context.Context, commentID, body string) error {
	author, room, event, err := s.Feed.CommentBridge(ctx, commentID)
	if err != nil || event == "" {
		return err
	}
	token, err := s.as(ctx, author, room)
	if err != nil {
		return err
	}
	_, err = s.M.Send(ctx, room, map[string]any{"msgtype": "m.text", "body": "* " + body, "go24.feedcomment": commentID,
		"m.new_content": map[string]any{"msgtype": "m.text", "body": body},
		"m.relates_to":  map[string]any{"rel_type": "m.replace", "event_id": event}}, token)
	return err
}

// PublishCommentDelete removes a comment from the post's thread first.
func (s *Service) PublishCommentDelete(ctx context.Context, commentID string) error {
	_, room, event, err := s.Feed.CommentBridge(ctx, commentID)
	if err != nil || event == "" {
		return err
	}
	return s.M.Redact(ctx, room, event, "go24")
}
