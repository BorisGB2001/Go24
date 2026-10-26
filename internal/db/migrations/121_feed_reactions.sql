-- Reactions of the feed: a like is one of the emotions (Нравится, Восхищаюсь, Смеюсь, Удивляюсь,
-- Грущу, Злюсь, Фейспалм), and comments of the feed have them too.
ALTER TABLE feed_likes
    ADD COLUMN reaction   text NOT NULL DEFAULT 'like' CHECK (reaction IN ('like', 'kiss', 'laugh', 'wonder', 'cry', 'angry', 'facepalm')),
    ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();

CREATE TABLE feed_comment_likes (
    comment_id   uuid NOT NULL REFERENCES feed_comments(id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reaction     text NOT NULL DEFAULT 'like' CHECK (reaction IN ('like', 'kiss', 'laugh', 'wonder', 'cry', 'angry', 'facepalm')),
    matrix_event text NOT NULL DEFAULT '', -- the m.reaction of the messenger bridge
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (comment_id, user_id)
);
CREATE INDEX feed_comment_likes_matrix_idx ON feed_comment_likes (matrix_event) WHERE matrix_event <> '';

-- the author learns which reaction it was (the body is its emoji); a comment's author — with the comment's text
CREATE FUNCTION go24_reaction_emoji(r text) RETURNS text AS $$
    SELECT CASE r WHEN 'kiss' THEN '😍' WHEN 'laugh' THEN '😂' WHEN 'wonder' THEN '😮' WHEN 'cry' THEN '😢'
        WHEN 'angry' THEN '😡' WHEN 'facepalm' THEN '🤦' ELSE '👍' END
$$ LANGUAGE sql IMMUTABLE;

CREATE OR REPLACE FUNCTION go24_feed_like_notification() RETURNS trigger AS $$
BEGIN
    IF NOT go24_quiet() THEN
        INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
        SELECT p.author_id, NEW.user_id, 'like', p.id, go24_reaction_emoji(NEW.reaction)
        FROM feed_posts p WHERE p.id = NEW.post_id AND p.author_id <> NEW.user_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE FUNCTION go24_feed_comment_like_notification() RETURNS trigger AS $$
BEGIN
    IF NOT go24_quiet() THEN
        INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
        SELECT c.author_id, NEW.user_id, 'comment_like', c.post_id, go24_reaction_emoji(NEW.reaction) || ' ' || left(c.body, 300)
        FROM feed_comments c WHERE c.id = NEW.comment_id AND c.author_id <> NEW.user_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER feed_comment_likes_notification AFTER INSERT ON feed_comment_likes
    FOR EACH ROW EXECUTE FUNCTION go24_feed_comment_like_notification();
