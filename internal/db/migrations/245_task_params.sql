-- Task parameters (SE_PARAMETER 1–3): «Сроки определяются сроками подзадач», «Автоматически завершать
-- задачу при завершении подзадач», «Не завершать задачу без результата». Templates carry them too.
ALTER TABLE tasks_all ADD COLUMN dates_from_subtasks boolean NOT NULL DEFAULT false,
    ADD COLUMN auto_complete boolean NOT NULL DEFAULT false,
    ADD COLUMN result_required boolean NOT NULL DEFAULT false;
CREATE OR REPLACE VIEW tasks AS SELECT * FROM tasks_all WHERE deleted_at IS NULL; -- the new columns (see 060)
ALTER TABLE task_templates ADD COLUMN dates_from_subtasks boolean NOT NULL DEFAULT false,
    ADD COLUMN auto_complete boolean NOT NULL DEFAULT false,
    ADD COLUMN result_required boolean NOT NULL DEFAULT false;

-- task_dates_from_subtasks: a task whose dates come from its subtasks takes the latest deadline, the earliest planned
-- start and the latest planned end of its subtasks outside the trash (a value no subtask has stays as it was).
-- Updating the task fires the trigger again for its own parent, so the dates go up the tree; nothing is written
-- when nothing changes, so the chain stops.
CREATE FUNCTION task_dates_from_subtasks(p uuid) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    dl timestamptz;
    sp date;
    ep date;
BEGIN
    SELECT coalesce(max(c.deadline), t.deadline), coalesce(min(c.start_plan), t.start_plan), coalesce(max(c.end_plan), t.end_plan)
    INTO dl, sp, ep
    FROM tasks_all t LEFT JOIN tasks_all c ON c.parent_id = t.id AND c.deleted_at IS NULL
    WHERE t.id = p AND t.dates_from_subtasks AND t.deleted_at IS NULL
    GROUP BY t.id, t.deadline, t.start_plan, t.end_plan
    HAVING count(c.id) > 0;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    IF ep < sp THEN -- one subtask has only a start, another only an earlier end
        ep := sp;
    END IF;
    UPDATE tasks_all SET deadline = dl, start_plan = sp, end_plan = ep, version = version + 1, updated_at = now()
    WHERE id = p AND (deadline IS DISTINCT FROM dl OR start_plan IS DISTINCT FROM sp OR end_plan IS DISTINCT FROM ep);
END $$;

CREATE FUNCTION task_dates_follow() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.parent_id IS NOT NULL THEN
        PERFORM task_dates_from_subtasks(NEW.parent_id);
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.parent_id IS NOT NULL AND OLD.parent_id IS DISTINCT FROM NEW.parent_id THEN
            PERFORM task_dates_from_subtasks(OLD.parent_id);
        END IF;
        IF NEW.dates_from_subtasks AND NOT OLD.dates_from_subtasks THEN
            PERFORM task_dates_from_subtasks(NEW.id);
        END IF;
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER task_dates_follow AFTER INSERT OR UPDATE OF deadline, start_plan, end_plan, parent_id, deleted_at, dates_from_subtasks
    ON tasks_all FOR EACH ROW EXECUTE FUNCTION task_dates_follow();
