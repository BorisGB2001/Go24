-- The feed in the messenger: the company feed is a Matrix room ("feed"), a project post goes to the project's
-- room; comments are the post's thread, likes are reactions. Edits and deletions follow in both directions.
ALTER TABLE matrix_rooms DROP CONSTRAINT matrix_rooms_kind_check;
ALTER TABLE matrix_rooms ADD CONSTRAINT matrix_rooms_kind_check CHECK (kind IN ('space', 'project', 'task', 'feed'));

ALTER TABLE feed_posts ADD COLUMN matrix_room text NOT NULL DEFAULT '', ADD COLUMN matrix_event text NOT NULL DEFAULT '',
    ADD COLUMN edited_at timestamptz;
CREATE INDEX feed_posts_matrix_idx ON feed_posts (matrix_event) WHERE matrix_event <> '';
ALTER TABLE feed_comments ADD COLUMN matrix_event text NOT NULL DEFAULT '';
CREATE INDEX feed_comments_matrix_idx ON feed_comments (matrix_event) WHERE matrix_event <> '';
ALTER TABLE feed_likes ADD COLUMN matrix_event text NOT NULL DEFAULT '';

-- live feed: new posts are announced like task chat messages
CREATE FUNCTION go24_feed_notify() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('go24_feed', 'feed:' || NEW.id::text);
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER feed_posts_notify AFTER INSERT ON feed_posts FOR EACH ROW EXECUTE FUNCTION go24_feed_notify();
