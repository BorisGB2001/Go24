-- «Открытые линии», the operator's tools: «Быстрые ответы» (own and the company's), «Спам» (the dialogue
-- is closed and the visitor's messages are no longer taken), live updates of the dialogue list and the open dialogue.
CREATE TABLE ol_quick (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   uuid REFERENCES users(id) ON DELETE CASCADE, -- NULL: the company's (administrators keep them)
    title      text NOT NULL CHECK (length(title) BETWEEN 1 AND 100),
    body       text NOT NULL CHECK (length(body) BETWEEN 1 AND 5000),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ol_quick_owner_idx ON ol_quick (owner_id);

ALTER TABLE ol_sessions ADD COLUMN spam boolean NOT NULL DEFAULT false;

-- every change of a dialogue (a message, its operator, status) is announced: open «Открытые линии» pages refresh
CREATE FUNCTION go24_openlines_notify() RETURNS trigger AS $$
BEGIN
    IF TG_TABLE_NAME = 'ol_messages' THEN
        PERFORM pg_notify('go24_openlines', 'ol:' || NEW.session_id::text);
    ELSE
        PERFORM pg_notify('go24_openlines', 'ol:' || NEW.id::text);
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER ol_messages_notify AFTER INSERT ON ol_messages FOR EACH ROW EXECUTE FUNCTION go24_openlines_notify();
CREATE TRIGGER ol_sessions_notify AFTER UPDATE OF status, operator_id, spam ON ol_sessions
    FOR EACH ROW EXECUTE FUNCTION go24_openlines_notify();
