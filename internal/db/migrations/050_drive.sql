-- «Диск»: «Мой диск», «Общий диск» of the company and the drive of each project, with folders. A file
-- lies in one drive (and maybe a folder); its bytes stay in the storage chosen on upload (built-in, Nextcloud, S3…).
CREATE TABLE drive_folders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    drive      text NOT NULL CHECK (drive IN ('user', 'company', 'project')),
    owner_id   uuid,               -- the user or the project; NULL for the company drive
    parent_id  uuid REFERENCES drive_folders(id) ON DELETE RESTRICT,
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200 AND name !~ '[/\\]'),
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX drive_folders_name_idx ON drive_folders (drive, coalesce(owner_id, '00000000-0000-0000-0000-000000000000'),
    coalesce(parent_id, '00000000-0000-0000-0000-000000000000'), lower(name));

ALTER TABLE files ADD COLUMN drive text CHECK (drive IN ('user', 'company', 'project')),
                  ADD COLUMN drive_owner uuid,
                  ADD COLUMN folder_id uuid REFERENCES drive_folders(id) ON DELETE RESTRICT;
CREATE INDEX files_drive_idx ON files (drive, drive_owner, folder_id);
-- personal uploads made so far are «Мой диск» of their uploader
UPDATE files SET drive = 'user', drive_owner = uploaded_by WHERE task_id IS NULL AND post_id IS NULL AND uploaded_by IS NOT NULL;
