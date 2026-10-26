-- Mail as in Outlook: «Импорт из файла» (GO24DEV-613): Outlook's personal folders (.pst), .mbox, a .zip of .mbox or
-- .eml files go into a mailbox — into the same folders (Входящие, Отправленные… by their roles, the rest made as a
-- tree) or into a chosen folder; go24 worker puts the letters on the server, the file stays on the user's drive.
CREATE TABLE mail_imports (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mailbox_id uuid NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    folder_id  uuid REFERENCES mail_folders(id) ON DELETE SET NULL, -- none: into the same folders
    file_id    uuid REFERENCES files(id) ON DELETE SET NULL,
    name       text NOT NULL DEFAULT '',
    format     text NOT NULL CHECK (format IN ('pst', 'mbox', 'zip', 'eml')),
    dupes      boolean NOT NULL DEFAULT false, -- letters already in the folder (by Message-ID) go once more
    lang       text NOT NULL DEFAULT '',
    status     text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    letters    integer NOT NULL DEFAULT 0, -- put on the server
    same       integer NOT NULL DEFAULT 0, -- left out: already in the folder
    skipped    integer NOT NULL DEFAULT 0, -- left out: no letters (contacts, meetings…) or refused by the server
    position   integer NOT NULL DEFAULT 0, -- letters of the file gone through: a restarted import goes on from here
    folders    integer NOT NULL DEFAULT 0,
    error      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX mail_imports_user_idx ON mail_imports (user_id, created_at DESC);
CREATE INDEX mail_imports_queue_idx ON mail_imports (created_at) WHERE status IN ('queued', 'running');
