-- «Публичная ссылка» of «Диск»: a file or a folder (with all it holds) opened by anyone who has the address
-- /s/{token}, until it expires; with a password, only after typing it.
CREATE TABLE drive_links (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token         text NOT NULL UNIQUE,
    file_id       uuid REFERENCES files(id) ON DELETE CASCADE,
    folder_id     uuid REFERENCES drive_folders(id) ON DELETE CASCADE,
    password_hash text NOT NULL DEFAULT '',
    expires_at    timestamptz,
    downloads     integer NOT NULL DEFAULT 0,
    created_by    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((file_id IS NULL) <> (folder_id IS NULL))
);
CREATE UNIQUE INDEX drive_links_file_idx ON drive_links (file_id) WHERE file_id IS NOT NULL;
CREATE UNIQUE INDEX drive_links_folder_idx ON drive_links (folder_id) WHERE folder_id IS NOT NULL;
