-- The feed has it: «Скрыть» takes a post out of one's own feed (it stays in «Скрытые» to be brought
-- back), «Просмотры» count who saw a post, and an important post may be important only until a date — then it
-- is an ordinary one (out of «Важные» and of the unread badge).
CREATE TABLE feed_hidden (
	post_id    uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (post_id, user_id)
);
CREATE INDEX feed_hidden_user ON feed_hidden (user_id);

CREATE TABLE feed_views (
	post_id   uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
	user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	viewed_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (post_id, user_id)
);

ALTER TABLE feed_posts ADD COLUMN important_until timestamptz;
