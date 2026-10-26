package feed

import "context"

// ProcessPost publishes a business process started by hand, as the «Процессы в ленте новостей»: a post of
// its starter, seen by them (and those the process gives a task to — see ProcessReader).
func (s *Store) ProcessPost(ctx context.Context, starterID, instanceID, title, body string) (string, error) {
	id, err := s.CreatePostTo(ctx, starterID, "", Audience{Users: []string{starterID}}, title, body, false)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx, `UPDATE feed_posts SET bp_instance = $2 WHERE id = $1`, id, instanceID)
	return id, err
}

// ProcessReader makes an employee an addressee of the posts of a process: they are given a task of it. They are told of
// the task by the process itself, not of the post.
func (s *Store) ProcessReader(ctx context.Context, instanceID, userID string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO feed_post_recipients (post_id, user_id)
		SELECT p.id, u.id FROM feed_posts p, users u WHERE p.bp_instance::text = $1 AND u.id::text = $2 AND u.active AND NOT u.guest
		  AND p.author_id <> u.id AND NOT EXISTS (SELECT 1 FROM feed_post_recipients r WHERE r.post_id = p.id AND r.user_id = u.id)`,
		instanceID, userID)
	return err
}
