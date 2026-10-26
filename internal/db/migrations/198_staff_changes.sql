-- «Кадровые изменения» (GO24DEV-418): an employee taken on (a guest made an employee), moved to other
-- departments, dismissed or returned; the names of the departments are kept as they were. Taken on before this
-- log are shown by users.employment_date (or the day they came to go24).
CREATE TABLE staff_changes (
    id          bigserial PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('hired', 'moved', 'fired', 'returned')),
    at          timestamptz NOT NULL DEFAULT now(),
    departments text[] NOT NULL DEFAULT '{}', -- after the change
    was         text[] NOT NULL DEFAULT '{}', -- before it (moved)
    actor_id    uuid REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX staff_changes_at_idx ON staff_changes (at DESC);
