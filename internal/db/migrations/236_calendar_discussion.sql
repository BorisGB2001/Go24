-- «Файлы» and «Комментарии» of a meeting, as on a meeting's page of the Календарь: the organizer attaches files to
-- the meeting, everyone who sees it discusses it below (a comment may carry files too). The organizer, the invited
-- and those who commented before hear of a new comment.
CREATE TABLE event_comments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id   uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    author_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    body       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX event_comments_event_idx ON event_comments (event_id, created_at);

-- a file of a meeting (event_comment_id — of its comment); a removed one stays its uploader's
ALTER TABLE files ADD COLUMN event_id uuid REFERENCES events(id) ON DELETE SET NULL,
    ADD COLUMN event_comment_id uuid REFERENCES event_comments(id) ON DELETE SET NULL;
CREATE INDEX files_event_idx ON files (event_id) WHERE event_id IS NOT NULL;
