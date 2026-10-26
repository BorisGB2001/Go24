-- Reactions on task chat messages (👍 ✅ 🔥 …), as in the messenger: set in go24 or in Element, kept in
-- Matrix (m.reaction), indexed here so the task card shows them; a change re-sends the message to open cards.
CREATE TABLE task_comment_reactions (
    comment_id   uuid NOT NULL REFERENCES task_comments(id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key          text NOT NULL CHECK (length(key) BETWEEN 1 AND 64),
    matrix_event text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (comment_id, user_id, key)
);
CREATE INDEX task_comment_reactions_event_idx ON task_comment_reactions (matrix_event) WHERE matrix_event <> '';

CREATE FUNCTION go24_reaction_notify() RETURNS trigger AS $$
DECLARE r task_comment_reactions;
BEGIN
    r := coalesce(NEW, OLD);
    PERFORM pg_notify('go24_comments', c.task_id::text || ':' || c.id::text) FROM task_comments c WHERE c.id = r.comment_id;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER task_comment_reactions_notify AFTER INSERT OR DELETE ON task_comment_reactions
    FOR EACH ROW EXECUTE FUNCTION go24_reaction_notify();
