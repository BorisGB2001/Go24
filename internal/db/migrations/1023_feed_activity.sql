-- The last activity of a feed post: a new comment lifts the post to the top of the feed sorted
-- «by activity». A post starts with its own time; each comment moves it to the comment's time (never back).
ALTER TABLE feed_posts ADD COLUMN activity_at timestamptz;
UPDATE feed_posts p SET activity_at = greatest(p.created_at,
	coalesce((SELECT max(c.created_at) FROM feed_comments c WHERE c.post_id = p.id), p.created_at));
ALTER TABLE feed_posts ALTER COLUMN activity_at SET NOT NULL, ALTER COLUMN activity_at SET DEFAULT now();
CREATE INDEX feed_posts_activity_idx ON feed_posts (activity_at DESC);

CREATE FUNCTION go24_feed_post_activity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	NEW.activity_at := NEW.created_at;
	RETURN NEW;
END $$;
CREATE TRIGGER feed_post_activity BEFORE INSERT ON feed_posts FOR EACH ROW EXECUTE FUNCTION go24_feed_post_activity();

CREATE FUNCTION go24_feed_comment_activity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	UPDATE feed_posts SET activity_at = NEW.created_at WHERE id = NEW.post_id AND activity_at < NEW.created_at;
	RETURN NULL;
END $$;
CREATE TRIGGER feed_comment_activity AFTER INSERT ON feed_comments FOR EACH ROW EXECUTE FUNCTION go24_feed_comment_activity();
