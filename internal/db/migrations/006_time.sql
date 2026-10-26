-- Time tracking: estimates, time entries (manual or from a timer), one running timer per user.
ALTER TABLE tasks ADD COLUMN estimate_minutes integer CHECK (estimate_minutes >= 0);

CREATE TABLE time_entries (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id),
    seconds    integer NOT NULL CHECK (seconds > 0 AND seconds <= 604800),
    spent_on   date NOT NULL,
    comment    text NOT NULL DEFAULT '',
    source     text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'timer')),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX time_entries_task_idx ON time_entries (task_id);
CREATE INDEX time_entries_user_idx ON time_entries (user_id, spent_on);

CREATE TABLE task_timers (
    user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    started_at timestamptz NOT NULL DEFAULT now()
);
