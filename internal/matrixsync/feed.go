package matrixsync

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

	"go24/internal/feed"
	"go24/internal/matrix"
)

// The feed in the messenger: company posts live in the "feed" room of the space (all employees), a project's
// posts in the project's room. A post's comments are its thread, reactions
// (to the post and to its comments) are m.reaction with the reaction's emoji. Everything made in
// go24 carries a "go24.*" key, so the bridge never copies its own events back.

func mine(content map[string]any) bool {
	for k := range content {
		if strings.HasPrefix(k, "go24.") {
			return true
		}
	}
	return false
}

// postText renders a post for Matrix: plain body and HTML with the title in bold.
func postText(b feed.Bridge) (string, string) {
	plain, rich := b.Body, html.EscapeString(b.Body)
	if b.Title != "" {
		plain = b.Title + "\n" + plain
		rich = "<b>" + html.EscapeString(b.Title) + "</b><br>" + rich
	}
	if b.Important {
		plain, rich = "❗ "+plain, "❗ "+rich
	}
	return plain, strings.ReplaceAll(rich, "\n", "<br>")
}

// postRoom: the feed room for company posts, the project's room for project posts ("" when it has none).
func (s *Service) postRoom(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return s.roomOf(ctx, "feed", nil)
	}
	return s.roomOf(ctx, "project", &projectID)
}

// as posts as the go24 user (their Matrix account), joining them to the room first.
func (s *Service) as(ctx context.Context, userID, room string) (string, error) {
	mxid, token, err := s.puppet(ctx, userID)
	if err != nil || mxid == "" {
		return "", err
	}
	if err := s.M.ForceJoin(ctx, room, mxid); err != nil && !matrix.IsCode(err, "M_FORBIDDEN") {
		return "", err
	}
	return token, nil
}

func (s *Service) logErr(what string, err error) {
	if err != nil {
		s.Log.Error("matrix feed: "+what, "err", err)
	}
}

// OnPost copies a new go24 post to the messenger.
func (s *Service) OnPost(ctx context.Context, postID string) {
	if s == nil {
		return
	}
	s.logErr("post", func() error {
		b, err := s.Feed.BridgeInfo(ctx, postID)
		if err != nil || b.Event != "" || b.Limited { // a post to chosen people is not for the company's room
			return err
		}
		room, err := s.postRoom(ctx, b.ProjectID)
		if err != nil || room == "" {
			return err
		}
		token, err := s.as(ctx, b.AuthorID, room)
		if err != nil {
			return err
		}
		plain, rich := postText(b)
		ev, err := s.M.Send(ctx, room, map[string]any{"msgtype": "m.text", "body": plain, "format": "org.matrix.custom.html",
			"formatted_body": rich, "go24.post": b.ID}, token)
		if err != nil {
			return err
		}
		return s.Feed.SetPostEvent(ctx, b.ID, room, ev)
	}())
}

// OnPostEdit sends the edit (m.replace) of a post.
func (s *Service) OnPostEdit(ctx context.Context, postID string) {
	if s == nil {
		return
	}
	s.logErr("edit", func() error {
		b, err := s.Feed.BridgeInfo(ctx, postID)
		if err != nil || b.Event == "" {
			return err
		}
		token, err := s.as(ctx, b.AuthorID, b.Room)
		if err != nil {
			return err
		}
		plain, rich := postText(b)
		_, err = s.M.Send(ctx, b.Room, map[string]any{"msgtype": "m.text", "body": "* " + plain, "go24.post": b.ID,
			"m.new_content": map[string]any{"msgtype": "m.text", "body": plain, "format": "org.matrix.custom.html", "formatted_body": rich},
			"m.relates_to":  map[string]any{"rel_type": "m.replace", "event_id": b.Event}}, token)
		return err
	}())
}

// OnPostDelete removes the post's message from the messenger (room and event read before the post is deleted).
func (s *Service) OnPostDelete(ctx context.Context, room, event string) {
	if s == nil || event == "" {
		return
	}
	s.logErr("delete", s.M.Redact(ctx, room, event, "go24"))
}

// OnFeedComment posts a comment into the post's thread.
func (s *Service) OnFeedComment(ctx context.Context, postID string, c feed.Comment) {
	if s == nil {
		return
	}
	s.logErr("comment", func() error {
		b, err := s.Feed.BridgeInfo(ctx, postID)
		if err != nil || b.Event == "" {
			return err
		}
		token, err := s.as(ctx, c.AuthorID, b.Room)
		if err != nil {
			return err
		}
		body := c.Body
		if token == "" { // no Matrix account: the bot posts with the name
			body = c.AuthorName + ": " + body
		}
		ev, err := s.M.Send(ctx, b.Room, map[string]any{"msgtype": "m.text", "body": body, "go24.feedcomment": c.ID,
			"m.relates_to": map[string]any{"rel_type": "m.thread", "event_id": b.Event, "is_falling_back": true,
				"m.in_reply_to": map[string]string{"event_id": b.Event}}}, token)
		if err != nil {
			return err
		}
		return s.Feed.SetCommentEvent(ctx, c.ID, ev)
	}())
}

// OnReact mirrors the user's reaction to a post (or to its comment, commentID) as an m.reaction with the
// reaction's emoji. A reaction that was changed or taken back is redacted before, by OnUnreact.
func (s *Service) OnReact(ctx context.Context, postID, commentID, userID string) {
	if s == nil {
		return
	}
	s.logErr("react", func() error {
		b, err := s.Feed.BridgeInfo(ctx, postID)
		if err != nil || b.Event == "" {
			return err
		}
		id, target := postID, b.Event
		if commentID != "" {
			id, target = commentID, s.Feed.CommentEvent(ctx, commentID)
		}
		reaction, _ := s.Feed.MyReaction(ctx, id, userID, commentID != "")
		if reaction == "" || target == "" {
			return nil
		}
		token, err := s.as(ctx, userID, b.Room)
		if err != nil || token == "" {
			return err
		}
		ev, err := s.M.SendEvent(ctx, b.Room, "m.reaction", map[string]any{"go24.like": true,
			"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": target, "key": feed.ReactionEmoji[reaction]}}, token)
		if err != nil {
			return err
		}
		return s.Feed.SetReactionEvent(ctx, id, userID, ev, commentID != "")
	}())
}

// OnUnreact redacts the messenger reaction of a reaction that is being changed or taken back.
func (s *Service) OnUnreact(ctx context.Context, postID, event string) {
	if s == nil || event == "" {
		return
	}
	b, err := s.Feed.BridgeInfo(ctx, postID)
	if err == nil {
		err = s.M.Redact(ctx, b.Room, event, "go24")
	}
	s.logErr("unreact", err)
}

// inboundFeed handles messenger events that belong to the feed. It reports whether the event was a feed one.
func (s *Service) inboundFeed(ctx context.Context, kind, roomID, userID string, ev matrix.Event) (bool, error) {
	rel, _ := ev.Content["m.relates_to"].(map[string]any)
	relType, _ := rel["rel_type"].(string)
	target, _ := rel["event_id"].(string)
	switch ev.Type {
	case "m.room.redaction":
		redacts := ev.Redacts
		if redacts == "" {
			redacts, _ = ev.Content["redacts"].(string)
		}
		what, id, owner, err := s.Feed.ByEvent(ctx, redacts)
		if err != nil || what == "" {
			return what != "", err
		}
		if owner != userID {
			return true, nil // only the author's own deletion is mirrored
		}
		switch what {
		case "post":
			return true, ignoreRights(s.Feed.DeletePost(ctx, userID, id))
		case "comment":
			return true, ignoreRights(s.Feed.DeleteComment(ctx, userID, id))
		case "like":
			return true, ignoreRights(s.Feed.SetReaction(ctx, userID, id, "", ""))
		case "clike":
			return true, ignoreRights(s.Feed.SetCommentReaction(ctx, userID, id, "", ""))
		}
	case "m.reaction":
		what, id, _, err := s.Feed.ByEvent(ctx, target)
		key, _ := rel["key"].(string)
		switch {
		case err != nil:
			return true, err
		case what == "post":
			return true, ignoreRights(s.Feed.SetReaction(ctx, userID, id, feed.ReactionOf(key), ev.EventID))
		case what == "comment":
			return true, ignoreRights(s.Feed.SetCommentReaction(ctx, userID, id, feed.ReactionOf(key), ev.EventID))
		}
		return what != "", nil
	case "m.room.message":
		body, _ := ev.Content["body"].(string)
		if relType == "m.replace" {
			nc, _ := ev.Content["m.new_content"].(map[string]any)
			nb, _ := nc["body"].(string)
			what, id, owner, err := s.Feed.ByEvent(ctx, target)
			if err != nil || what == "" || owner != userID {
				return what != "", err
			}
			switch what {
			case "post":
				title, text := splitTitle(nb)
				return true, ignoreRights(s.Feed.EditPost(ctx, userID, id, title, text))
			case "comment":
				return true, ignoreRights(s.Feed.EditComment(ctx, userID, id, stripReplyFallback(nb)))
			}
			return true, nil
		}
		root := ""
		if relType == "m.thread" {
			root = target
		}
		if root != "" { // a reply in a post's thread is a comment
			what, postID, _, err := s.Feed.ByEvent(ctx, root)
			if err != nil || what != "post" {
				return false, err
			}
			mentions, text, err := s.inboundMentions(ctx, ev.Content, stripReplyFallback(body))
			if err != nil {
				return true, err
			}
			c, err := s.Feed.AddComment(ctx, userID, postID, text, mentions...)
			if err != nil {
				return true, ignoreRights(err)
			}
			return true, s.Feed.SetCommentEvent(ctx, c.ID, ev.EventID)
		}
		if kind == "feed" && strings.TrimSpace(body) != "" { // a new message in the feed room is a post
			mentions, plain, err := s.inboundMentions(ctx, ev.Content, stripReplyFallback(body))
			if err != nil {
				return true, err
			}
			title, text := splitTitle(plain)
			id, err := s.Feed.CreatePost(ctx, userID, "", title, text, false, mentions...)
			if errors.Is(err, feed.ErrNoPostAll) { // not one who may write to all employees: the message leaves the room
				s.logErr("feed rights", s.M.Redact(ctx, roomID, ev.EventID, "go24"))
				return true, nil
			}
			if err != nil {
				return true, ignoreRights(err)
			}
			return true, s.Feed.SetPostEvent(ctx, id, roomID, ev.EventID)
		}
	}
	return false, nil
}

// splitTitle: a first line in bold ("**Title**") or a short first line followed by text is the post's title.
func splitTitle(body string) (string, string) {
	body = strings.TrimSpace(strings.TrimPrefix(body, "❗ "))
	first, rest, ok := strings.Cut(body, "\n")
	if !ok || strings.TrimSpace(rest) == "" {
		return "", body
	}
	if t := strings.Trim(first, "* "); strings.HasPrefix(first, "**") && t != "" {
		return t, strings.TrimSpace(rest)
	}
	return "", body
}

func ignoreRights(err error) error {
	if errors.Is(err, feed.ErrForbidden) || errors.Is(err, feed.ErrNotFound) {
		return nil
	}
	return err
}

var errNoRoom = fmt.Errorf("matrix: the post's room does not exist yet")
