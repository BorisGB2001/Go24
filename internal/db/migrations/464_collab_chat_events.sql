-- «Уведомления в коллабе» (GO24DEV-707), as «Уведомления в проекте»: which events of the collab's log the
-- go24 bot tells in the collab's chat — people (added, removed, left, a new role, a new owner), guests joining,
-- invitations (made, sent again, revoked), the collab itself (settings, name, archive). All are on by default.
ALTER TABLE projects
    ADD COLUMN events_members boolean NOT NULL DEFAULT true,
    ADD COLUMN events_guests boolean NOT NULL DEFAULT true,
    ADD COLUMN events_invites boolean NOT NULL DEFAULT true,
    ADD COLUMN events_collab boolean NOT NULL DEFAULT true;

-- the line of the chat a log entry became: NULL — not told yet, '' — not to be told (off, or before this)
ALTER TABLE collab_log ADD COLUMN chat_event text;
UPDATE collab_log SET chat_event = '';
CREATE INDEX collab_log_chat_idx ON collab_log (id) WHERE chat_event IS NULL;

CREATE FUNCTION collab_log_notify() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('go24_collab_log', NEW.project_id::text);
    RETURN NULL;
END $$;
CREATE TRIGGER collab_log_notify AFTER INSERT ON collab_log FOR EACH ROW EXECUTE FUNCTION collab_log_notify();
