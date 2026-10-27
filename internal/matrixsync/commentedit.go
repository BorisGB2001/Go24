package matrixsync

import (
	"context"
	"errors"

	"go24/internal/matrix"
	"go24/internal/tasks"
)

// Changed and deleted task chat messages: the edit (m.replace) and the redaction travel both ways.

// commentEvent is the messenger event of a message of the task ("" when it is not in the messenger).
func (s *Service) commentEvent(ctx context.Context, taskID, commentID string) string {
	if commentID == "" {
		return ""
	}
	var ev string
	_ = s.DB.QueryRow(ctx, `SELECT e.event_id FROM matrix_events e JOIN task_comments c ON c.id = e.comment_id
		WHERE c.id::text = $1 AND c.task_id = $2 ORDER BY e.event_id LIMIT 1`, commentID, taskID).Scan(&ev)
	return ev
}

// EditTaskComment sends the new text of the person's message to the messenger, then changes it in go24.
func (s *Service) EditTaskComment(ctx context.Context, userID, taskID, commentID, body string) (tasks.Comment, error) {
	if err := s.Tasks.CommentRights(ctx, userID, taskID, commentID, true); err != nil {
		return tasks.Comment{}, err
	}
	if ev := s.commentEvent(ctx, taskID, commentID); ev != "" && body != "" {
		room, _, err := s.target(ctx, taskID)
		if err != nil {
			return tasks.Comment{}, err
		}
		token, err := s.as(ctx, userID, room)
		if err != nil {
			return tasks.Comment{}, err
		}
		if token != "" { // the bot cannot change a message of someone else: go24 keeps the edit of people without an account
			if _, err := s.M.Send(ctx, room, map[string]any{"msgtype": "m.text", "body": "* " + body, "go24.comment": commentID,
				"m.new_content": map[string]any{"msgtype": "m.text", "body": body},
				"m.relates_to":  map[string]any{"rel_type": "m.replace", "event_id": ev}}, token); err != nil {
				return tasks.Comment{}, err
			}
		}
	}
	return s.Tasks.EditComment(ctx, userID, taskID, commentID, body)
}

// DeleteTaskComment removes the message from the messenger (the bot redacts it), then from go24.
func (s *Service) DeleteTaskComment(ctx context.Context, userID, taskID, commentID string) error {
	if err := s.Tasks.CommentRights(ctx, userID, taskID, commentID, false); err != nil {
		return err
	}
	if ev := s.commentEvent(ctx, taskID, commentID); ev != "" {
		room, _, err := s.target(ctx, taskID)
		if err != nil {
			return err
		}
		if err := s.M.Redact(ctx, room, ev, "go24"); err != nil && !matrix.IsCode(err, "M_NOT_FOUND") {
			return err
		}
	}
	return s.Tasks.DeleteComment(ctx, userID, taskID, commentID)
}

// inboundCommentChange mirrors an edit or a deletion of a task chat message made in the messenger. It reports
// whether the event was one.
func (s *Service) inboundCommentChange(ctx context.Context, userID string, ev matrix.Event) (bool, error) {
	var target string
	switch ev.Type {
	case "m.room.redaction":
		if target = ev.Redacts; target == "" {
			target, _ = ev.Content["redacts"].(string)
		}
	case "m.room.message":
		rel, _ := ev.Content["m.relates_to"].(map[string]any)
		if t, _ := rel["rel_type"].(string); t != "m.replace" {
			return false, nil
		}
		target, _ = rel["event_id"].(string)
	default:
		return false, nil
	}
	taskID, commentID, err := s.Tasks.CommentByEvent(ctx, target)
	if err != nil || commentID == "" {
		return commentID != "", err
	}
	if ev.Type == "m.room.redaction" { // the messenger has let them remove it (its own rights): go24 follows
		return true, ignoreTaskRights(s.Tasks.DeleteComment(tasks.WithAdmin(ctx), userID, taskID, commentID))
	}
	nc, _ := ev.Content["m.new_content"].(map[string]any)
	body, _ := nc["body"].(string)
	if body == "" {
		return true, nil
	}
	_, err = s.Tasks.EditComment(ctx, userID, taskID, commentID, body)
	return true, ignoreTaskRights(err)
}

func ignoreTaskRights(err error) error {
	if errors.Is(err, tasks.ErrForbidden) || errors.Is(err, tasks.ErrNotFound) {
		return nil
	}
	return err
}
