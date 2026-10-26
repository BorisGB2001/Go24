-- Every change of a task (not only its status) goes to the task's messenger thread as a log line from the go24 bot;
-- people cannot delete the bot's lines there. Changes made before this stay as they were.
UPDATE task_history SET matrix_event = '' WHERE matrix_event IS NULL;
DROP INDEX IF EXISTS task_history_status_log_idx;
CREATE INDEX task_history_log_idx ON task_history (id) WHERE matrix_event IS NULL;

CREATE OR REPLACE FUNCTION go24_status_notify() RETURNS trigger AS $$
BEGIN
    IF NEW.field <> 'created' THEN
        PERFORM pg_notify('go24_status', NEW.id::text);
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
