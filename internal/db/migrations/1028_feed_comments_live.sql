-- Live comments of an open feed: a new comment is announced with its post and its own ID
-- ("feedc:<post id>/<comment id>" — the menu counter only counts the event), an edited or deleted one as
-- "feedce:edit/<comment id>" or "feedce:del/<comment id>", so the open feeds change it in place.
CREATE OR REPLACE FUNCTION go24_feed_comment_notify() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('go24_feed', 'feedc:' || NEW.post_id::text || '/' || NEW.id::text);
    RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE FUNCTION go24_feed_comment_change_notify() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM pg_notify('go24_feed', 'feedce:del/' || OLD.id::text);
        RETURN OLD;
    END IF;
    PERFORM pg_notify('go24_feed', 'feedce:edit/' || NEW.id::text);
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER feed_comments_live_edit AFTER UPDATE OF body ON feed_comments FOR EACH ROW
    WHEN (OLD.body IS DISTINCT FROM NEW.body) EXECUTE FUNCTION go24_feed_comment_change_notify();
CREATE TRIGGER feed_comments_live_delete AFTER DELETE ON feed_comments FOR EACH ROW
    EXECUTE FUNCTION go24_feed_comment_change_notify();
