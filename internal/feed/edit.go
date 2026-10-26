package feed

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Изменить» a post as a whole: the form of a new post — addressees, «Важное» and its term, files,
// the gratitude and the poll (until somebody votes). The caller checks CanChange first.

// Draft is a post as its edit form shows it.
type Draft struct {
	Post
	ProjectID string // the post's project, "" — all employees or chosen addressees
	Limited   bool   // to chosen people and departments
	Voted     bool   // its poll has votes: it can no longer change
}

// Draft returns the post for its edit form (CanChange).
func (s *Store) Draft(ctx context.Context, userID, postID string) (Draft, error) {
	if err := s.CanChange(ctx, userID, postID); err != nil {
		return Draft{}, err
	}
	items, err := s.list(ctx, userID, Filter{}, postID)
	if err != nil {
		return Draft{}, err
	}
	if len(items) == 0 || items[0].Post == nil {
		return Draft{}, ErrNotFound
	}
	d := Draft{Post: *items[0].Post}
	err = s.db.QueryRow(ctx, `SELECT coalesce(project_id::text, ''), limited, EXISTS (SELECT 1 FROM feed_poll_votes v WHERE v.post_id = p.id)
		FROM feed_posts p WHERE id = $1`, postID).Scan(&d.ProjectID, &d.Limited, &d.Voted)
	return d, err
}

// SetAudience readdresses the post: to a project its author works in, to chosen people and departments, or (both
// empty) to all employees. A project post keeps those it was shared with while it stays in that project. The new
// readers of a post to chosen people, or of an important one, are told (told: who). moved: the post changes its
// messenger room (a post to chosen people has none).
func (s *Store) SetAudience(ctx context.Context, actorID, postID, projectID string, to Audience) (moved bool, told []string, err error) {
	limited := len(to.Users)+len(to.Depts) > 0
	if limited && projectID != "" {
		return false, nil, i18n.E("err.bad_form")
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var author, oldProject string
		var oldLimited bool
		if err := tx.QueryRow(ctx, `SELECT author_id, coalesce(project_id::text, ''), limited FROM feed_posts WHERE id = $1 FOR UPDATE`,
			postID).Scan(&author, &oldProject, &oldLimited); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if projectID != "" && projectID != oldProject {
			var member bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2 AND role <> 'viewer')`,
				projectID, author).Scan(&member); err != nil {
				return err
			}
			if !member && !tasks.IsAdmin(ctx) {
				return ErrForbidden
			}
		}
		moved = oldProject != projectID || oldLimited != limited
		if moved && projectID == "" && !limited { // to all employees now: by the rights of the one who changes it
			if err := s.checkRights(ctx, actorID, true, false); err != nil {
				return err
			}
		}
		var before []string
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(u.id::text), '{}') FROM users u JOIN feed_posts p ON p.id = $1
			WHERE u.active AND `+reader, postID).Scan(&before); err != nil {
			return err
		}
		var project any
		if projectID != "" {
			project = projectID
		}
		if _, err := tx.Exec(ctx, `UPDATE feed_posts SET project_id = $2, limited = $3, updated_at = now() WHERE id = $1`, postID, project, limited); err != nil {
			return err
		}
		if limited || projectID != oldProject || oldLimited { // the shares of a project post stay in it
			if _, err := tx.Exec(ctx, `DELETE FROM feed_post_recipients WHERE post_id = $1`, postID); err != nil {
				return err
			}
		}
		if limited {
			if _, err := tx.Exec(ctx, `INSERT INTO feed_post_recipients (post_id, user_id)
				SELECT $1, u.id FROM users u WHERE u.id::text = ANY($2::text[]) AND u.active AND NOT u.guest`, postID, to.Users); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO feed_post_recipients (post_id, department_id)
				SELECT $1, d.id FROM departments d WHERE d.id::text = ANY($2::text[])`, postID, to.Depts); err != nil {
				return err
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM feed_post_recipients WHERE post_id = $1`, postID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return i18n.E("err.post_to")
			}
		}
		rows, err := tx.Query(ctx, `INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
			SELECT u.id, $2, CASE WHEN p.important THEN 'important' ELSE 'post_to_you' END, p.id, left(coalesce(nullif(p.title, ''), p.body), 300)
			FROM users u JOIN feed_posts p ON p.id = $1
			WHERE (p.limited OR p.important) AND u.active AND u.id <> p.author_id AND u.id <> $2
			  AND NOT (u.id::text = ANY($3::text[])) AND `+reader+` AND NOT go24_quiet()
			RETURNING user_id::text`, postID, actorID, before)
		if err != nil {
			return err
		}
		told, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return moved, told, err
}

// SetImportant makes the post important (until the moment; zero — always) or an ordinary one; the confirmations
// «Прочитано» stay. changed: it became important or ordinary (TellImportant then tells its readers).
func (s *Store) SetImportant(ctx context.Context, actorID, postID string, important bool, until time.Time) (changed bool, err error) {
	var at any
	if important && !until.IsZero() {
		if !until.After(time.Now()) {
			return false, i18n.E("err.until_past")
		}
		at = until
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var was bool
		if err := tx.QueryRow(ctx, `SELECT important FROM feed_posts WHERE id = $1 FOR UPDATE`, postID).Scan(&was); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		changed = was != important
		if changed && important {
			if err := s.checkRights(ctx, actorID, false, true); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE feed_posts SET important = $2, important_until = $3 WHERE id = $1`, postID, important, at)
		return err
	})
	return changed, err
}

// TellImportant tells the readers of a post that became important (but those already told).
func (s *Store) TellImportant(ctx context.Context, actorID, postID string, except []string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
		SELECT u.id, $2, 'important', p.id, left(coalesce(nullif(p.title, ''), p.body), 300) FROM users u JOIN feed_posts p ON p.id = $1
		WHERE p.important AND u.active AND u.id <> p.author_id AND u.id <> $2 AND NOT (u.id::text = ANY(coalesce($3::text[], '{}')))
		  AND `+reader+` AND NOT go24_quiet()`, postID, actorID, except)
	return err
}

// DetachFiles takes files off the post (they stay among the uploader's «Загруженные файлы»).
func (s *Store) DetachFiles(ctx context.Context, postID string, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, `UPDATE files SET post_id = NULL WHERE post_id = $1 AND id::text = ANY($2::text[])`, postID, fileIDs)
	return err
}

// FileCount is how many files the post carries.
func (s *Store) FileCount(ctx context.Context, postID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM files WHERE post_id = $1`, postID).Scan(&n)
	return n, err
}

// EditPoll replaces the post's poll while nobody voted (an empty question removes it).
func (s *Store) EditPoll(ctx context.Context, postID, question string, options []string, kind PollKind) error {
	question = strings.TrimSpace(question)
	var opts []string
	if question != "" {
		var err error
		if opts, err = pollOptions(question, options); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var voted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM feed_poll_votes WHERE post_id = $1)`, postID).Scan(&voted); err != nil {
			return err
		}
		if voted {
			return i18n.E("err.poll_voted")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM feed_polls WHERE post_id = $1`, postID); err != nil {
			return err
		}
		if question == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO feed_polls (post_id, question, multiple, anonymous) VALUES ($1, $2, $3, $4)`,
			postID, question, kind.Multiple, kind.Anonymous); err != nil {
			return err
		}
		for i, o := range opts {
			if _, err := tx.Exec(ctx, `INSERT INTO feed_poll_options (post_id, text, sort) VALUES ($1, $2, $3)`, postID, o, i); err != nil {
				return err
			}
		}
		return nil
	})
}

// EditGratitude changes the post's gratitude: its badge and the people thanked (the newly thanked are told);
// no badge — the post is no gratitude any more.
func (s *Store) EditGratitude(ctx context.Context, actorID, postID, badge string, people []string) error {
	if badge != "" && (!slices.Contains(Badges, badge) || len(people) == 0) {
		return i18n.E("err.gratitude")
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var before []string
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(user_id::text), '{}') FROM feed_gratitude_users WHERE post_id = $1`,
			postID).Scan(&before); err != nil {
			return err
		}
		if badge == "" {
			_, err := tx.Exec(ctx, `DELETE FROM feed_gratitudes WHERE post_id = $1`, postID)
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO feed_gratitudes (post_id, badge) VALUES ($1, $2)
			ON CONFLICT (post_id) DO UPDATE SET badge = excluded.badge`, postID, badge); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM feed_gratitude_users WHERE post_id = $1`, postID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO feed_gratitude_users (post_id, user_id)
			SELECT $1, u.id FROM users u JOIN feed_posts p ON p.id = $1 WHERE u.id::text = ANY($2) AND u.active AND u.id <> p.author_id`, postID, people)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return i18n.E("err.gratitude")
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, post_id)
			SELECT user_id, $2, 'gratitude', $1 FROM feed_gratitude_users
			WHERE post_id = $1 AND user_id <> $2 AND NOT (user_id::text = ANY($3::text[])) AND NOT go24_quiet()`, postID, actorID, before)
		return err
	})
}

// Unbridge forgets the post's messages in the messenger (the post's and its comments'), before they are removed
// there: their removal is then no deletion in go24. Returns the room and the events.
func (s *Store) Unbridge(ctx context.Context, postID string) (room string, events []string, err error) {
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var post string
		if err := tx.QueryRow(ctx, `SELECT matrix_room, matrix_event FROM feed_posts WHERE id = $1 FOR UPDATE`, postID).Scan(&room, &post); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(matrix_event), '{}') FROM feed_comments WHERE post_id = $1 AND matrix_event <> ''`,
			postID).Scan(&events); err != nil {
			return err
		}
		if post != "" {
			events = append([]string{post}, events...)
		}
		if _, err := tx.Exec(ctx, `UPDATE feed_comments SET matrix_event = '' WHERE post_id = $1`, postID); err != nil {
			return err
		}
		// the reactions there go with the messages
		if _, err := tx.Exec(ctx, `UPDATE feed_likes SET matrix_event = '' WHERE post_id = $1`, postID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE feed_comment_likes SET matrix_event = '' WHERE comment_id IN (SELECT id FROM feed_comments WHERE post_id = $1)`, postID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE feed_posts SET matrix_room = '', matrix_event = '' WHERE id = $1`, postID)
		return err
	})
	return room, events, err
}

// BridgeComments are the post's comments, oldest first, for writing them anew into the messenger.
func (s *Store) BridgeComments(ctx context.Context, postID string) ([]Comment, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.author_id::text, u.name, c.body, c.created_at FROM feed_comments c
		JOIN users u ON u.id = c.author_id WHERE c.post_id = $1 ORDER BY c.created_at`, postID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Comment, error) {
		var c Comment
		return c, r.Scan(&c.ID, &c.AuthorID, &c.AuthorName, &c.Body, &c.CreatedAt)
	})
}
