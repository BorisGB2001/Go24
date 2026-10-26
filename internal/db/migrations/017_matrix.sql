-- Messenger on the go24 Matrix server. go24 is the source of truth: it creates the employees' Matrix accounts,
-- the company space, a private room per project and a thread per task (or a private room per task where only
-- task participants may see it), keeps the members in step and deactivates dismissed employees.
ALTER TABLE users ADD COLUMN username text NOT NULL DEFAULT ''; -- Authentik login (preferred_username)

CREATE TABLE matrix_rooms (
    kind       text NOT NULL CHECK (kind IN ('space', 'project', 'task')),
    ref_id     uuid,                -- project or task; NULL for the company space
    room_id    text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX matrix_rooms_ref_idx ON matrix_rooms (kind, coalesce(ref_id, '00000000-0000-0000-0000-000000000000'));

ALTER TABLE tasks ADD COLUMN matrix_thread text NOT NULL DEFAULT ''; -- thread root event in the project room

-- go24 user ↔ Matrix account; token: a login of that user made by the bot (admin API), to post as them.
CREATE TABLE matrix_users (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    mxid    text NOT NULL UNIQUE,
    token   text NOT NULL DEFAULT ''
);

-- Messages that crossed the bridge, so nothing is copied twice.
CREATE TABLE matrix_events (
    event_id   text PRIMARY KEY,
    comment_id uuid NOT NULL REFERENCES task_comments(id) ON DELETE CASCADE
);
CREATE INDEX matrix_events_comment_idx ON matrix_events (comment_id);
