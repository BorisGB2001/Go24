-- Repeating meetings and reminders (the portal «Повторяемость» and «Напоминание» of a calendar event).
ALTER TABLE events
    ADD COLUMN repeat         text NOT NULL DEFAULT '' CHECK (repeat IN ('', 'daily', 'weekly', 'monthly', 'yearly')),
    ADD COLUMN repeat_every   integer NOT NULL DEFAULT 1 CHECK (repeat_every BETWEEN 1 AND 365),
    ADD COLUMN repeat_until   date,      -- NULL = no end
    ADD COLUMN remind_minutes integer CHECK (remind_minutes >= 0); -- NULL = no reminder

-- which occurrences have been reminded of already
CREATE TABLE event_reminders (
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    at       timestamptz NOT NULL, -- the occurrence's start
    PRIMARY KEY (event_id, at)
);
