-- Task status changes go to the task's messenger thread as a log line («13.10.2026 10:05 — Claude завершил задачу»),
-- and a completed task gets a ✅ reaction on its thread header (removed when it is reopened).
ALTER TABLE task_history ADD COLUMN matrix_event text;           -- NULL: not in the messenger yet
UPDATE task_history SET matrix_event = '' WHERE field = 'status'; -- what happened before stays as it was
CREATE INDEX task_history_status_log_idx ON task_history (id) WHERE field = 'status' AND matrix_event IS NULL;
ALTER TABLE tasks ADD COLUMN matrix_done text NOT NULL DEFAULT ''; -- the ✅ reaction event

CREATE FUNCTION go24_status_notify() RETURNS trigger AS $$
BEGIN
    IF NEW.field = 'status' THEN
        PERFORM pg_notify('go24_status', NEW.id::text);
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER task_history_status_log AFTER INSERT ON task_history FOR EACH ROW EXECUTE FUNCTION go24_status_notify();
