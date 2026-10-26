package matrixsync

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"go24/internal/matrix"
)

// Reactions on task chat messages: kept in Matrix (m.reaction from the person), indexed in go24.

// ReactComment puts the person's reaction on a task chat message, or takes it back when it is there already.
func (s *Service) ReactComment(ctx context.Context, userID, taskID, commentID, key string) error {
	has, err := s.Tasks.HasReaction(ctx, userID, commentID, key)
	if err != nil {
		return err
	}
	if has {
		old, err := s.Tasks.SetReaction(ctx, userID, taskID, commentID, key, false, "")
		if err != nil || old == "" {
			return err
		}
		room, _, err := s.target(ctx, taskID)
		if err != nil {
			return err
		}
		return s.M.Redact(ctx, room, old, "")
	}
	if err := s.Tasks.CheckVisible(ctx, userID, taskID); err != nil {
		return err
	}
	var target string
	err = s.DB.QueryRow(ctx, `SELECT event_id FROM matrix_events WHERE comment_id = $1`, commentID).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) { // a message not in the messenger (yet): only go24 knows the reaction
		_, err = s.Tasks.SetReaction(ctx, userID, taskID, commentID, key, true, "")
		return err
	}
	if err != nil {
		return err
	}
	room, _, err := s.target(ctx, taskID)
	if err != nil {
		return err
	}
	token, err := s.as(ctx, userID, room)
	if err != nil {
		return err
	}
	ev := ""
	if token != "" { // people without a messenger account react in go24 only
		if ev, err = s.M.SendEvent(ctx, room, "m.reaction", map[string]any{"m.relates_to": map[string]any{
			"rel_type": "m.annotation", "event_id": target, "key": key}}, token); err != nil {
			return err
		}
	}
	_, err = s.Tasks.SetReaction(ctx, userID, taskID, commentID, key, true, ev)
	return err
}

// inboundReaction mirrors a reaction (or its removal) on a task chat message from the messenger.
// It reports whether the event was about such a reaction.
func (s *Service) inboundReaction(ctx context.Context, userID string, ev matrix.Event) (bool, error) {
	switch ev.Type {
	case "m.reaction":
		rel, _ := ev.Content["m.relates_to"].(map[string]any)
		target, _ := rel["event_id"].(string)
		key, _ := rel["key"].(string)
		var commentID, taskID string
		err := s.DB.QueryRow(ctx, `SELECT c.id::text, c.task_id::text FROM matrix_events e JOIN task_comments c ON c.id = e.comment_id
			WHERE e.event_id = $1`, target).Scan(&commentID, &taskID)
		if errors.Is(err, pgx.ErrNoRows) || key == "" {
			return false, nil
		}
		if err != nil {
			return true, err
		}
		_, err = s.Tasks.SetReaction(ctx, userID, taskID, commentID, key, true, ev.EventID)
		return true, ignoreRights(err)
	case "m.room.redaction":
		redacts := ev.Redacts
		if redacts == "" {
			redacts, _ = ev.Content["redacts"].(string)
		}
		return s.Tasks.ReactionByEvent(ctx, redacts)
	}
	return false, nil
}
