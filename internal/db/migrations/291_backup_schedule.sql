-- «Резервное копирование» (GO24DEV-571): copies made by the schedule, and the old ones removed (only the folder:
-- the journal keeps the line) when more than the chosen number are kept.
ALTER TABLE backup_runs ADD COLUMN auto boolean NOT NULL DEFAULT false;
ALTER TABLE backup_runs ADD COLUMN removed_at timestamptz;
