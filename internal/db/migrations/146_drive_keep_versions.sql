-- «Диск: место»: how many former versions of a file are kept (kind 'versions', the count in bytes; 0 — all of them)
ALTER TABLE drive_limits DROP CONSTRAINT drive_limits_kind_check;
ALTER TABLE drive_limits ADD CONSTRAINT drive_limits_kind_check CHECK (kind IN ('user', 'company', 'project', 'versions'));
