-- User-connected storages (rclone remotes managed through rclone rcd).
-- owner_id NULL = shared storage created by an administrator, visible to everyone.
CREATE TABLE storages (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    type          text NOT NULL CHECK (type IN ('webdav', 'sftp', 's3', 'ftp')),
    remote        text NOT NULL UNIQUE,              -- rclone remote name, generated: go24_<hex>
    root          text NOT NULL DEFAULT '',          -- folder / bucket inside the remote
    summary       text NOT NULL DEFAULT '',          -- non-secret description (host, user) for the UI
    owner_id      uuid REFERENCES users(id) ON DELETE CASCADE,
    created_by    uuid NOT NULL REFERENCES users(id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    checked_at    timestamptz,
    check_ok      boolean NOT NULL DEFAULT false,
    check_error   text NOT NULL DEFAULT ''
);
CREATE INDEX storages_owner_idx ON storages (owner_id);

-- Files: where they live (NULL = built-in storage) and which task they are attached to.
ALTER TABLE files ADD COLUMN storage_id uuid REFERENCES storages(id) ON DELETE RESTRICT;
ALTER TABLE files ADD COLUMN task_id uuid REFERENCES tasks(id) ON DELETE SET NULL;
ALTER TABLE files ADD COLUMN legacy_id integer UNIQUE;
CREATE INDEX files_task_idx ON files (task_id);
CREATE INDEX files_uploaded_by_idx ON files (uploaded_by);
