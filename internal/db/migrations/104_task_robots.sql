-- «Роботы» of project stages task automation: when a task enters a kanban stage, go24 does what the
-- stage's robots say (notify, comment, change people, deadline, status or stage, add a checklist item, make a task).
-- Any stage change — a drag on the kanban, a status change, a new task — queues the robots of the new stage here;
-- the worker runs them (a robot may wait, the task must still be in the stage then).
CREATE TABLE task_robots (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stage_id   uuid NOT NULL REFERENCES project_stages(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('notify', 'comment', 'responsible', 'accomplice', 'auditor', 'deadline',
                                             'status', 'stage', 'checklist', 'task')),
    params     jsonb NOT NULL DEFAULT '{}',
    delay_sec  integer NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX task_robots_stage_idx ON task_robots (stage_id) WHERE active;

CREATE TABLE task_robot_jobs (
    id       bigserial PRIMARY KEY,
    robot_id uuid NOT NULL REFERENCES task_robots(id) ON DELETE CASCADE,
    task_id  uuid NOT NULL REFERENCES tasks_all(id) ON DELETE CASCADE,
    stage_id uuid NOT NULL,
    depth    integer NOT NULL DEFAULT 0, -- how many robots moved the task in a row (a ring of stage robots stops)
    run_at   timestamptz NOT NULL
);
CREATE INDEX task_robot_jobs_due_idx ON task_robot_jobs (run_at);

-- A task without a stage sits in the project's first stage: a new one enters it, and placing it there later is no move.
CREATE FUNCTION task_robots_enqueue() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    st    uuid := NEW.stage_id;
    first uuid;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.stage_id IS NOT DISTINCT FROM OLD.stage_id THEN
        RETURN NEW;
    END IF;
    SELECT id INTO first FROM project_stages WHERE project_id = NEW.project_id AND code IS DISTINCT FROM 'finish'
        ORDER BY sort, created_at LIMIT 1;
    IF st IS NULL THEN
        IF TG_OP = 'UPDATE' THEN
            RETURN NEW;
        END IF;
        st := first;
    ELSIF TG_OP = 'UPDATE' AND OLD.stage_id IS NULL AND st = first THEN
        RETURN NEW;
    END IF;
    IF st IS NULL THEN
        RETURN NEW;
    END IF;
    INSERT INTO task_robot_jobs (robot_id, task_id, stage_id, depth, run_at)
    SELECT r.id, NEW.id, st, coalesce(nullif(current_setting('go24.robot_depth', true), ''), '0')::int,
           now() + make_interval(secs => r.delay_sec)
    FROM task_robots r WHERE r.stage_id = st AND r.active;
    RETURN NEW;
END $$;
CREATE TRIGGER task_robots_enqueue AFTER INSERT OR UPDATE OF stage_id ON tasks_all
    FOR EACH ROW EXECUTE FUNCTION task_robots_enqueue();
