-- «Рабочее время»: a changed start, end or pauses of a working day, with the reason. An employee's change
-- waits for the manager's approval (pending); a manager's or administrator's change, and an approved one, is applied
-- and stays as the history of the day.
CREATE TABLE workday_changes (
    id          bigserial PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day         date NOT NULL,
    author_id   uuid REFERENCES users(id) ON DELETE SET NULL,
    started_at  timestamptz,           -- the new values; NULL — not changed
    ended_at    timestamptz,
    pause_secs  int CHECK (pause_secs >= 0),
    old_started timestamptz,           -- the day before the change (NULL: there was no day)
    old_ended   timestamptz,
    old_pause   int,
    reason      text NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'declined')),
    decided_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    decided_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX workday_changes_day_idx ON workday_changes (user_id, day);
CREATE UNIQUE INDEX workday_changes_pending_idx ON workday_changes (user_id, day) WHERE status = 'pending';
