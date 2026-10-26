-- «Обсуждение» of a file of a drive, as on a file's page of the Диск: comments under the file's card. Its
-- uploader and those who commented on it before hear of a new one.
CREATE TABLE drive_file_comments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id    uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    author_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    body       text NOT NULL CHECK (body <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX drive_file_comments_file_idx ON drive_file_comments (file_id, created_at);
