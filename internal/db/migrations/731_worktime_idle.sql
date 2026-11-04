-- «Контроль времени» timeman (GO24DEV-858): during a running working day the pages of go24 tell the
-- server the person is active; a gap of the set minutes and more is an absence, the person says where they were —
-- on business or personal — and managers see the absences in a report.
ALTER TABLE workdays ADD COLUMN active_at timestamptz; -- the last activity of the running day (NULL — not tracked yet)
CREATE TABLE worktime_idle (
    id          bigserial PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day         date NOT NULL,
    started_at  timestamptz NOT NULL,
    ended_at    timestamptz NOT NULL CHECK (ended_at > started_at),
    kind        text NOT NULL DEFAULT '' CHECK (kind IN ('', 'work', 'personal')), -- '' — not answered yet
    reason      text NOT NULL DEFAULT '' CHECK (length(reason) <= 1000),
    answered_at timestamptz
);
CREATE INDEX worktime_idle_user ON worktime_idle (user_id, day);
