package tasks

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The task chat: an answer quotes the message it answers, the author changes («изменено») or
// deletes their own message, an administrator deletes any.

// commentCols and commentJoins read a chat message (alias c) with the quote of the one it answers.
const commentCols = `c.id, c.author_id, u.name, c.body, c.created_at,
	coalesce(c.reply_to::text, ''), coalesce(ru.name, ''), coalesce(left(rc.body, 200), ''), c.edited_at`

const commentJoins = `JOIN users u ON u.id = c.author_id
	LEFT JOIN task_comments rc ON rc.id = c.reply_to LEFT JOIN users ru ON ru.id = rc.author_id`

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func commentByID(ctx context.Context, q querier, taskID, id string) (Comment, error) {
	if !uuidRe.MatchString(id) {
		return Comment{}, ErrNotFound
	}
	rows, err := q.Query(ctx, `SELECT `+commentCols+` FROM task_comments c `+commentJoins+`
		WHERE c.id = $1 AND c.task_id = $2`, id, taskID)
	if err != nil {
		return Comment{}, err
	}
	c, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Comment])
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CommentRights tells whether the person may change (edit) or delete the message: only its author changes it,
// the author or an administrator deletes it; nobody in a collab's archive. The task must be visible to them.
func (s *Store) CommentRights(ctx context.Context, userID, taskID, id string, edit bool) error {
	return s.commentRights(ctx, s.db, userID, taskID, id, edit)
}

func (s *Store) commentRights(ctx context.Context, q querier, userID, taskID, id string, edit bool) error {
	a, err := loadAccess(ctx, q, userID, taskID, false)
	if err != nil {
		return err
	}
	c, err := commentByID(ctx, q, taskID, id)
	if err != nil {
		return err
	}
	if a.Frozen || (c.AuthorID != userID && (edit || !IsAdmin(ctx))) {
		return ErrForbidden
	}
	return nil
}

// EditComment changes the text of the person's own message.
func (s *Store) EditComment(ctx context.Context, userID, taskID, id, body string) (Comment, error) {
	var c Comment
	body = strings.TrimSpace(body)
	if body == "" {
		return c, invalid("err.comment_empty")
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.commentRights(ctx, tx, userID, taskID, id, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE task_comments SET body = $3, edited_at = now(), updated_at = now()
			WHERE id = $1 AND task_id = $2 AND body <> $3`, id, taskID, body); err != nil {
			return err
		}
		var err error
		c, err = commentByID(ctx, tx, taskID, id)
		return err
	})
	return c, err
}

// DeleteComment removes a message (with its reactions and the «Результат» mark); answers to it lose the quote.
func (s *Store) DeleteComment(ctx context.Context, userID, taskID, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.commentRights(ctx, tx, userID, taskID, id, false); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM task_comments WHERE id = $1 AND task_id = $2`, id, taskID)
		return err
	})
}

// CommentByEvent finds the task chat message a messenger event carries ("" when it is not one).
func (s *Store) CommentByEvent(ctx context.Context, event string) (taskID, commentID string, err error) {
	err = s.db.QueryRow(ctx, `SELECT c.task_id::text, c.id::text FROM matrix_events e JOIN task_comments c ON c.id = e.comment_id
		WHERE e.event_id = $1`, event).Scan(&taskID, &commentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return taskID, commentID, err
}
