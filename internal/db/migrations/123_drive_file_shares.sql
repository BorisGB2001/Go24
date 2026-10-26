-- «Поделиться» a single file of «Мой диск»: a share is of a folder or of a file (read | edit); the file
-- is in «Общие со мной» of those it is shared with and opens as a drive of its own (/disk/sf/FILE).
ALTER TABLE drive_shares
  ALTER COLUMN folder_id DROP NOT NULL,
  ADD COLUMN file_id uuid REFERENCES files(id) ON DELETE CASCADE,
  ADD CONSTRAINT drive_shares_item_check CHECK ((folder_id IS NULL) <> (file_id IS NULL)),
  ADD CONSTRAINT drive_shares_file_access_check CHECK (file_id IS NULL OR access IN ('read', 'edit'));
CREATE UNIQUE INDEX drive_shares_file_user_idx ON drive_shares (file_id, user_id) WHERE file_id IS NOT NULL AND user_id IS NOT NULL;
CREATE UNIQUE INDEX drive_shares_file_dept_idx ON drive_shares (file_id, department_id) WHERE file_id IS NOT NULL AND department_id IS NOT NULL;
