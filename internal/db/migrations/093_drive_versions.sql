-- «История версий» of «Диск»: a file uploaded again under its name into its folder keeps its address, links and
-- place, and what it was becomes a version — to download or to bring back.
CREATE TABLE drive_file_versions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id      uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    object_key   text NOT NULL,
    storage_id   uuid REFERENCES storages(id) ON DELETE RESTRICT,
    size         bigint NOT NULL,
    content_type text NOT NULL,
    uploaded_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL
);
CREATE INDEX drive_file_versions_file_idx ON drive_file_versions (file_id, created_at);

-- who uploaded the file's current version (NULL — its first uploader)
ALTER TABLE files ADD COLUMN changed_by uuid REFERENCES users(id) ON DELETE SET NULL;
