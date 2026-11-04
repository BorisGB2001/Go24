-- «Восстановить удалённые элементы», as in Outlook (GO24DEV-919): a letter deleted for good (in «Удалённые» or
-- «Нежелательная почта», «Очистить папку») is kept by go24 for RecoverDays — its source as the server had it — and can
-- be put back to the folder it was deleted from. folder — the folder's name on the server, role — its role (the
-- folder gone, the letter goes to the mailbox's folder of the role).
CREATE TABLE mail_recoverable (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    folder     text NOT NULL,
    role       text NOT NULL DEFAULT '',
    from_addr  text NOT NULL DEFAULT '',
    from_name  text NOT NULL DEFAULT '',
    subject    text NOT NULL DEFAULT '',
    sent_at    timestamptz NOT NULL,
    seen       boolean NOT NULL DEFAULT true,
    flagged    boolean NOT NULL DEFAULT false,
    source     bytea NOT NULL,
    deleted_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mail_recoverable_box_idx ON mail_recoverable (mailbox_id, deleted_at DESC);
