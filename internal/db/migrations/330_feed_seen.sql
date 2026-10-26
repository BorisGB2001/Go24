-- «Лента» counter of the menu: when each person last opened the feed — the posts and comments of
-- others since then are new (counted in the menu, marked in the feed).
CREATE TABLE feed_seen (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    seen_at timestamptz NOT NULL DEFAULT now()
);

-- live counters: a new comment is announced with its post ("feedc:<post id>"), like new posts
CREATE FUNCTION go24_feed_comment_notify() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('go24_feed', 'feedc:' || NEW.post_id::text);
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER feed_comments_live AFTER INSERT ON feed_comments FOR EACH ROW EXECUTE FUNCTION go24_feed_comment_notify();
