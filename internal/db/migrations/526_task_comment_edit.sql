-- Task chat: a message answers another one (quoted above it), its author may change it («изменено»)
-- or delete it. A change or a deletion is announced like a new message, so open cards show it at once.
ALTER TABLE task_comments ADD COLUMN reply_to uuid REFERENCES task_comments(id) ON DELETE SET NULL,
    ADD COLUMN edited_at timestamptz;

CREATE FUNCTION go24_comment_change_notify() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('go24_comments', OLD.task_id::text || ':' || OLD.id::text);
    RETURN NULL;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER task_comments_change_notify AFTER UPDATE OF body OR DELETE ON task_comments
    FOR EACH ROW EXECUTE FUNCTION go24_comment_change_notify();
