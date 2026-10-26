-- Deadline rules of a task (b_tasks.ALLOW_CHANGE_DEADLINE, MATCH_WORK_TIME):
--   allow_change_deadline — the responsible may move the deadline (otherwise only the creator and managers);
--   match_work_time       — deadline and plan dates skip weekends, holidays and off-hours of the company schedule.
ALTER TABLE tasks ADD COLUMN allow_change_deadline boolean NOT NULL DEFAULT false;
ALTER TABLE tasks ADD COLUMN match_work_time boolean NOT NULL DEFAULT false;

-- Company-wide settings edited by administrators (work schedule and the like), one JSON value per key.
CREATE TABLE company_settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
