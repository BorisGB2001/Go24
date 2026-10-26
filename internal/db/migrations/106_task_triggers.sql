-- «Триггеры» of project stages task automation: an event of a task (its status changed, a comment came,
-- the responsible or the deadline changed, the whole checklist is done, the deadline passed) moves it to the trigger's
-- stage. The events queue the moves here; the worker (and go24 right after the change) makes them, as robots run.
CREATE TABLE task_triggers (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stage_id   uuid NOT NULL REFERENCES project_stages(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('status', 'comment', 'responsible', 'deadline', 'checklist', 'overdue')),
    status     text NOT NULL DEFAULT '', -- a status trigger: the status the task came to
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX task_triggers_stage_idx ON task_triggers (stage_id);

CREATE TABLE task_trigger_jobs (
    id         bigserial PRIMARY KEY,
    trigger_id uuid NOT NULL REFERENCES task_triggers(id) ON DELETE CASCADE,
    task_id    uuid NOT NULL REFERENCES tasks_all(id) ON DELETE CASCADE,
    depth      integer NOT NULL DEFAULT 0
);

-- «Срок истёк» moves a task once per deadline: a new deadline lets it fire again.
CREATE TABLE task_trigger_fired (
    trigger_id uuid NOT NULL REFERENCES task_triggers(id) ON DELETE CASCADE,
    task_id    uuid NOT NULL REFERENCES tasks_all(id) ON DELETE CASCADE,
    PRIMARY KEY (trigger_id, task_id)
);

CREATE FUNCTION task_triggers_queue(task uuid, ev text, st text) RETURNS void LANGUAGE sql AS $$
    INSERT INTO task_trigger_jobs (trigger_id, task_id, depth)
    SELECT g.id, t.id, coalesce(nullif(current_setting('go24.robot_depth', true), ''), '0')::int
    FROM tasks_all t JOIN project_stages s ON s.project_id = t.project_id JOIN task_triggers g ON g.stage_id = s.id
    WHERE t.id = task AND t.deleted_at IS NULL AND g.kind = ev AND (ev <> 'status' OR g.status = st)
      AND g.stage_id IS DISTINCT FROM t.stage_id;
$$;

CREATE FUNCTION task_triggers_task() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM OLD.status THEN
        PERFORM task_triggers_queue(NEW.id, 'status', NEW.status::text);
    END IF;
    IF NEW.responsible_id IS DISTINCT FROM OLD.responsible_id THEN
        PERFORM task_triggers_queue(NEW.id, 'responsible', '');
    END IF;
    IF NEW.deadline IS DISTINCT FROM OLD.deadline THEN
        DELETE FROM task_trigger_fired WHERE task_id = NEW.id;
        PERFORM task_triggers_queue(NEW.id, 'deadline', '');
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER task_triggers_task AFTER UPDATE OF status, responsible_id, deadline ON tasks_all
    FOR EACH ROW EXECUTE FUNCTION task_triggers_task();

CREATE FUNCTION task_triggers_comment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM task_triggers_queue(NEW.task_id, 'comment', '');
    RETURN NEW;
END $$;
CREATE TRIGGER task_triggers_comment AFTER INSERT ON task_comments
    FOR EACH ROW EXECUTE FUNCTION task_triggers_comment();

CREATE FUNCTION task_triggers_checklist() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.done AND NOT OLD.done AND NOT EXISTS (SELECT 1 FROM task_checklist_items WHERE task_id = NEW.task_id AND NOT done) THEN
        PERFORM task_triggers_queue(NEW.task_id, 'checklist', '');
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER task_triggers_checklist AFTER UPDATE OF done ON task_checklist_items
    FOR EACH ROW EXECUTE FUNCTION task_triggers_checklist();
