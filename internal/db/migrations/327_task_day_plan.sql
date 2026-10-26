-- «План на день» of the working day (task.planner): the person's tasks for today, kept until removed;
-- a completed task leaves the plan the day after it was completed.
CREATE TABLE task_day_plan (
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_id  uuid NOT NULL REFERENCES tasks_all(id) ON DELETE CASCADE,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, task_id)
);
CREATE INDEX task_day_plan_task_idx ON task_day_plan (task_id);
