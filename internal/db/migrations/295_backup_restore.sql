-- «Резервное копирование» (GO24DEV-574): restoring from a copy is a line of the same journal (one at a time with the
-- copies): source — the copy restored, parts — what of it, secret — the copy's password (for its secrets) sealed by
-- the server's key until go24-worker takes it, safety — a copy of how things are is made first.
ALTER TABLE backup_runs ADD COLUMN kind text NOT NULL DEFAULT 'backup' CHECK (kind IN ('backup', 'restore'));
ALTER TABLE backup_runs ADD COLUMN source bigint REFERENCES backup_runs(id) ON DELETE SET NULL;
ALTER TABLE backup_runs ADD COLUMN secret text NOT NULL DEFAULT '';
ALTER TABLE backup_runs ADD COLUMN safety boolean NOT NULL DEFAULT false;
