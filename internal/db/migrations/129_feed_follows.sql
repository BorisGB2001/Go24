-- «Следить / Не следить» for a feed post has it: a new comment is told to everyone who follows the
-- post — its author, those who commented on it or were mentioned in it, and those who chose «Следить».
-- No row: only the author follows; follow = false is «Не следить». The comment notices are now made by the feed
-- store (feed.AddComment), so the trigger that told the author alone goes.
CREATE TABLE feed_follows (
	post_id uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
	user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	follow  boolean NOT NULL,
	PRIMARY KEY (post_id, user_id)
);
INSERT INTO feed_follows (post_id, user_id, follow)
SELECT DISTINCT c.post_id, c.author_id, true FROM feed_comments c;

DROP TRIGGER feed_comments_notification ON feed_comments;
DROP FUNCTION go24_feed_comment_notification();
