-- «Напоминания» of a portal task: a person sets a reminder for themselves, the responsible or the creator — at a
-- time, or some minutes before the deadline (it follows the deadline when that moves).
CREATE TABLE task_reminders (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id        uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- who set it (sees and removes it)
    recipient      text NOT NULL CHECK (recipient IN ('self', 'responsible', 'creator')),
    remind_at      timestamptz,
    before_minutes int CHECK (before_minutes > 0),
    sent_at        timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CHECK ((remind_at IS NULL) <> (before_minutes IS NULL))
);
CREATE INDEX task_reminders_task_idx ON task_reminders (task_id, user_id);
CREATE INDEX task_reminders_due_idx ON task_reminders (task_id) WHERE sent_at IS NULL;

-- a muted task notifies nothing but mentions and reminders the person asked for
CREATE OR REPLACE FUNCTION go24_notification_filter() RETURNS trigger AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE id = NEW.user_id AND NEW.kind = ANY(notify_off)) THEN
        RETURN NULL;
    END IF;
    IF NEW.task_id IS NOT NULL AND NEW.kind NOT IN ('mention', 'task_remind')
       AND EXISTS (SELECT 1 FROM task_marks WHERE user_id = NEW.user_id AND task_id = NEW.task_id AND mark = 'mute') THEN
        RETURN NULL;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
