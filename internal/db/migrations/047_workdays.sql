-- «Рабочее время» (timeman): an employee starts the working day, pauses it, finishes it with a short
-- report; managers see their people's days.
CREATE TABLE workdays (
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day         date NOT NULL,
    started_at  timestamptz NOT NULL,
    ended_at    timestamptz,
    paused_at   timestamptz,           -- on a pause since
    pause_secs  int NOT NULL DEFAULT 0, -- finished pauses
    report      text NOT NULL DEFAULT '' CHECK (length(report) <= 5000),
    PRIMARY KEY (user_id, day)
);
CREATE INDEX workdays_day_idx ON workdays (day);
