-- Live task chat: every new message is announced with NOTIFY, so each go24 instance can push it to the open
-- task pages at once (whoever wrote it: a colleague in go24 or someone in the messenger).
CREATE FUNCTION go24_comment_notify() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('go24_comments', NEW.task_id::text || ':' || NEW.id::text);
    RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER task_comments_notify AFTER INSERT ON task_comments
    FOR EACH ROW EXECUTE FUNCTION go24_comment_notify();
