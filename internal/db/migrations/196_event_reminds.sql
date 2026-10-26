-- Several reminders of a meeting («Напомнить» + «Добавить напоминание»): minutes before the start of each.
ALTER TABLE events ADD COLUMN reminds integer[] NOT NULL DEFAULT '{}' CHECK (0 <= ALL (reminds));
UPDATE events SET reminds = ARRAY[remind_minutes] WHERE remind_minutes IS NOT NULL;

-- an occurrence is reminded of once per reminder
ALTER TABLE event_reminders ADD COLUMN minutes integer NOT NULL DEFAULT 0;
UPDATE event_reminders r SET minutes = e.remind_minutes FROM events e WHERE e.id = r.event_id AND e.remind_minutes IS NOT NULL;
ALTER TABLE event_reminders DROP CONSTRAINT event_reminders_pkey, ADD PRIMARY KEY (event_id, at, minutes);

ALTER TABLE events DROP COLUMN remind_minutes;
