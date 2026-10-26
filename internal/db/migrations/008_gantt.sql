-- Gantt: finish-to-start links between tasks (the portal task.dependence, OpenProject precedes/follows).
CREATE TABLE task_links (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    from_task  uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, -- predecessor
    to_task    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, -- successor
    type       text NOT NULL DEFAULT 'finish_start' CHECK (type IN ('finish_start')),
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (from_task, to_task),
    CHECK (from_task <> to_task)
);
CREATE INDEX task_links_to_idx ON task_links (to_task);
ALTER TABLE tasks ADD CONSTRAINT tasks_plan_dates_chk CHECK (end_plan IS NULL OR start_plan IS NULL OR end_plan >= start_plan);
