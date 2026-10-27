-- The people mentioned in a post or its comments — the feed's «Упоминания». Filled when the mention
-- is made; the mentions made before are taken from the notifications that are still kept.
CREATE TABLE feed_mentions (
    post_id    uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, post_id)
);
CREATE INDEX feed_mentions_post_idx ON feed_mentions (post_id);

INSERT INTO feed_mentions (post_id, user_id, created_at)
SELECT post_id, user_id, min(created_at) FROM notifications WHERE kind = 'mention' AND post_id IS NOT NULL
GROUP BY post_id, user_id
ON CONFLICT DO NOTHING;
