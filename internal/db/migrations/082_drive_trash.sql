-- «Корзина» of «Диск»: a deleted file or folder is kept, with when and by whom, until restored, deleted for good or
-- kept longer than the company's trash setting. A folder goes there with all it holds (only the folder is marked).
ALTER TABLE files ADD COLUMN deleted_at timestamptz,
                  ADD COLUMN deleted_by uuid REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE drive_folders ADD COLUMN deleted_at timestamptz,
                          ADD COLUMN deleted_by uuid REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX files_deleted_idx ON files (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX drive_folders_deleted_idx ON drive_folders (deleted_at) WHERE deleted_at IS NOT NULL;
-- a folder in the trash does not keep its name from a new one
DROP INDEX drive_folders_name_idx;
CREATE UNIQUE INDEX drive_folders_name_idx ON drive_folders (drive, coalesce(owner_id, '00000000-0000-0000-0000-000000000000'),
    coalesce(parent_id, '00000000-0000-0000-0000-000000000000'), lower(name)) WHERE deleted_at IS NULL;
