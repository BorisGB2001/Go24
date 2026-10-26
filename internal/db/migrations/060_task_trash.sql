-- Task trash (Корзина): deleting a task moves it to the trash with its subtasks, comments, checklist
-- and files; it can be restored as it was or deleted for good (by hand or after N days, company setting).
--
-- The table becomes tasks_all and «tasks» is a view of the tasks not in the trash, so every query of go24 keeps
-- seeing only live tasks. The view is auto-updatable: INSERT/UPDATE (with ON CONFLICT and FOR UPDATE) go through it.
-- Foreign keys and triggers stay on tasks_all. A new column of a task is added to tasks_all and the view is made
-- again (CREATE OR REPLACE VIEW tasks AS SELECT * FROM tasks_all WHERE deleted_at IS NULL) — «SELECT *» of a view is
-- expanded when it is made.
ALTER TABLE tasks RENAME TO tasks_all;
ALTER TABLE tasks_all
    ADD COLUMN deleted_at   timestamptz,
    ADD COLUMN deleted_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN deleted_root uuid; -- the task the user deleted: its subtasks go to the trash and come back with it
CREATE INDEX tasks_all_trash_idx ON tasks_all (deleted_root) WHERE deleted_at IS NOT NULL;

CREATE VIEW tasks AS SELECT * FROM tasks_all WHERE deleted_at IS NULL;

-- outgoing webhooks: task.deleted (the portal OnTaskDelete) when a task goes to the trash
CREATE OR REPLACE FUNCTION go24_webhook_task() RETURNS trigger AS $$
DECLARE
    data jsonb;
BEGIN
    data := jsonb_build_object('id', NEW.id, 'key', (SELECT key FROM projects WHERE id = NEW.project_id) || '-' || NEW.number,
        'title', NEW.title, 'status', NEW.status, 'responsible_id', NEW.responsible_id, 'creator_id', NEW.creator_id, 'deadline', NEW.deadline);
    IF TG_OP = 'INSERT' THEN
        PERFORM go24_webhook('task.created', data);
    ELSIF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
        IF NEW.id = NEW.deleted_root THEN -- subtasks go with it
            PERFORM go24_webhook('task.deleted', data);
        END IF;
    ELSIF NEW.status IS DISTINCT FROM OLD.status AND NEW.status = 'completed' THEN
        PERFORM go24_webhook('task.completed', data);
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
