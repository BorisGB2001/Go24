-- «Заблокировать» a file of «Диск»: while it is locked, only the one who locked it (or those who manage
-- the drive) may change, rename, move or delete it.
ALTER TABLE files ADD COLUMN locked_by uuid REFERENCES users(id) ON DELETE SET NULL, ADD COLUMN locked_at timestamptz;
