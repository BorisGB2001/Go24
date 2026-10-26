package feed

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Опрос» of a feed post: answers, one or several choices, the votes of everyone who sees the post.

// Poll is a post's poll as the viewer sees it.
type Poll struct {
	Question string
	Multiple bool
	Options  []PollOption
	Voters   int  // people who voted
	Voted    bool // the viewer has voted
}

type PollOption struct {
	ID    string
	Text  string
	Votes int
	Mine  bool
}

// Percent is the option's share of the voters.
func (p Poll) Percent(o PollOption) int {
	if p.Voters == 0 {
		return 0
	}
	return o.Votes * 100 / p.Voters
}

// maxPollOptions bounds the answers of a poll.
const maxPollOptions = 20

// AddPoll puts a poll into the author's new post: a question and 2–20 different answers.
func (s *Store) AddPoll(ctx context.Context, authorID, postID, question string, options []string, multiple bool) error {
	question = strings.TrimSpace(question)
	opts, err := pollOptions(question, options)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO feed_polls (post_id, question, multiple)
		SELECT id, $3, $4 FROM feed_posts WHERE id = $1 AND author_id = $2`, postID, authorID, question, multiple)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrForbidden
	}
	for i, o := range opts {
		if _, err := tx.Exec(ctx, `INSERT INTO feed_poll_options (post_id, text, sort) VALUES ($1, $2, $3)`, postID, o, i); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// pollOptions checks a poll (the question trimmed): the different answers, 2–20 of them.
func pollOptions(question string, options []string) ([]string, error) {
	var opts []string
	for _, o := range options {
		if o = strings.TrimSpace(o); o != "" && !slices.Contains(opts, o) {
			opts = append(opts, o)
		}
	}
	if question == "" || len([]rune(question)) > 300 || len(opts) < 2 || len(opts) > maxPollOptions {
		return nil, i18n.E("err.poll")
	}
	for _, o := range opts {
		if len([]rune(o)) > 200 {
			return nil, i18n.E("err.poll")
		}
	}
	return opts, nil
}

// Vote replaces the person's vote (no options — the vote is taken back).
func (s *Store) Vote(ctx context.Context, userID, postID string, optionIDs []string) error {
	if err := s.canSee(ctx, userID, postID); err != nil {
		return err
	}
	var multiple bool
	if err := s.db.QueryRow(ctx, `SELECT multiple FROM feed_polls WHERE post_id = $1`, postID).Scan(&multiple); err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if len(optionIDs) > 1 && !multiple {
		return i18n.E("err.poll_one")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM feed_poll_votes WHERE post_id = $1 AND user_id = $2`, postID, userID); err != nil {
		return err
	}
	for _, o := range optionIDs {
		tag, err := tx.Exec(ctx, `INSERT INTO feed_poll_votes (option_id, post_id, user_id)
			SELECT id, post_id, $3 FROM feed_poll_options WHERE id::text = $1 AND post_id = $2 ON CONFLICT DO NOTHING`, o, postID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return i18n.E("err.bad_form")
		}
	}
	return tx.Commit(ctx)
}

// fillPolls sets the polls of the posts as the viewer sees them.
func (s *Store) fillPolls(ctx context.Context, viewer string, ids []string, idx map[string]*Post) error {
	rows, err := s.db.Query(ctx, `SELECT q.post_id::text, q.question, q.multiple, o.id, o.text,
			(SELECT count(*) FROM feed_poll_votes v WHERE v.option_id = o.id)::int,
			EXISTS (SELECT 1 FROM feed_poll_votes v WHERE v.option_id = o.id AND v.user_id::text = $2),
			(SELECT count(DISTINCT v.user_id) FROM feed_poll_votes v WHERE v.post_id = q.post_id)::int
		FROM feed_polls q JOIN feed_poll_options o ON o.post_id = q.post_id
		WHERE q.post_id = ANY($1::uuid[]) ORDER BY o.sort`, ids, viewer)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, question string
		var multiple bool
		var o PollOption
		var voters int
		if err := rows.Scan(&pid, &question, &multiple, &o.ID, &o.Text, &o.Votes, &o.Mine, &voters); err != nil {
			return err
		}
		p := idx[pid]
		if p.Poll == nil {
			p.Poll = &Poll{Question: question, Multiple: multiple, Voters: voters}
		}
		p.Poll.Options = append(p.Poll.Options, o)
		p.Poll.Voted = p.Poll.Voted || o.Mine
	}
	return rows.Err()
}

// CheckPoll tells whether a poll would do, before anything of its post changes.
func CheckPoll(question string, options []string) error {
	_, err := pollOptions(strings.TrimSpace(question), options)
	return err
}
