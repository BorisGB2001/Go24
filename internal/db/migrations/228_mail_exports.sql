-- Mail as in Outlook: «Экспорт» of letters to a file (GO24DEV-511): a mailbox, its folders (with subfolders) and a
-- period go as .mbox (mboxrd, a file per folder) or as .eml files in a .zip, made by go24 worker from the letters on
-- the server, put on the user's drive; the user is notified with a link.
CREATE TABLE mail_exports (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mailbox_id  uuid NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    folders     uuid[] NOT NULL DEFAULT '{}', -- none: the whole mailbox
    subfolders  boolean NOT NULL DEFAULT true,
    since       date,
    until       date,
    format      text NOT NULL CHECK (format IN ('mbox', 'eml')),
    lang        text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    letters     integer NOT NULL DEFAULT 0,
    size        bigint NOT NULL DEFAULT 0,
    file_id     uuid REFERENCES files(id) ON DELETE SET NULL,
    url         text NOT NULL DEFAULT '',
    error       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX mail_exports_user_idx ON mail_exports (user_id, created_at DESC);
CREATE INDEX mail_exports_queue_idx ON mail_exports (created_at) WHERE status IN ('queued', 'running');
