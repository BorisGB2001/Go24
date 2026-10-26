-- «Офисные сети» (GO24DEV-709): where a working day was started and finished timeman shows — the
-- address and the office network it was in ('' — outside the offices, NULL — no office networks were set).
ALTER TABLE workdays ADD COLUMN start_ip text NOT NULL DEFAULT '';
ALTER TABLE workdays ADD COLUMN start_office text;
ALTER TABLE workdays ADD COLUMN end_ip text NOT NULL DEFAULT '';
ALTER TABLE workdays ADD COLUMN end_office text;
