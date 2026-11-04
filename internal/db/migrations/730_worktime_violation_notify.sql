-- «Уведомлять о нарушениях» work schedules (GO24DEV-866): a late start, an early finish or a short day
-- is told to the person's manager and/or the chosen people; the company's rules keep theirs in the «worktime» setting.
ALTER TABLE work_schedules ADD COLUMN notify_head boolean NOT NULL DEFAULT false;
ALTER TABLE work_schedules ADD COLUMN notify_to uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE workdays ADD COLUMN violations_told text[] NOT NULL DEFAULT '{}'; -- violations already told: one notification each
