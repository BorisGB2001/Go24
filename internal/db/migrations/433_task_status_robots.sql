-- «Роботы» of «Мои задачи»: a person's own robots on the task statuses. They run in the tasks the
-- person has set (the creator) when such a task comes to the status — a new one comes to «Ждёт выполнения».
-- A robot sits either on a project stage (stage_id) or on a status of its owner (owner_id, status).
ALTER TABLE task_robots
    ALTER COLUMN stage_id DROP NOT NULL,
    ADD COLUMN owner_id uuid REFERENCES users(id) ON DELETE CASCADE,
    ADD COLUMN status text CHECK (status IN ('pending', 'in_progress', 'awaiting_control', 'completed', 'deferred')),
    ADD CONSTRAINT task_robots_place CHECK ((stage_id IS NOT NULL AND owner_id IS NULL AND status IS NULL)
        OR (stage_id IS NULL AND owner_id IS NOT NULL AND status IS NOT NULL));
CREATE INDEX task_robots_owner_idx ON task_robots (owner_id, status) WHERE active AND owner_id IS NOT NULL;

-- a job of a status robot keeps the status: the task must still be in it when the wait is over
ALTER TABLE task_robot_jobs ALTER COLUMN stage_id DROP NOT NULL, ADD COLUMN status text;

CREATE FUNCTION task_status_robots_enqueue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RETURN NEW;
    END IF;
    INSERT INTO task_robot_jobs (robot_id, task_id, status, depth, run_at)
    SELECT r.id, NEW.id, NEW.status, coalesce(nullif(current_setting('go24.robot_depth', true), ''), '0')::int,
           now() + make_interval(secs => r.delay_sec)
    FROM task_robots r WHERE r.owner_id = NEW.creator_id AND r.status = NEW.status AND r.active;
    RETURN NEW;
END $$;
CREATE TRIGGER task_status_robots_enqueue AFTER INSERT OR UPDATE OF status ON tasks_all
    FOR EACH ROW EXECUTE FUNCTION task_status_robots_enqueue();
