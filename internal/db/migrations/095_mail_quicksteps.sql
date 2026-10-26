-- Mail as in Outlook: «Игнорировать беседу» — the conversations of a mailbox whose letters, those there and those to
-- come, go to «Удалённые»; «Быстрые действия» (Quick Steps) — the user's sets of actions done by one button.
CREATE TABLE mail_ignored (
    mailbox_id uuid NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    thread     text NOT NULL CHECK (thread <> ''),  -- mail_messages.thread (a letter without one: its message_id)
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mailbox_id, thread)
);

CREATE TABLE mail_quicksteps (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    pos     int NOT NULL DEFAULT 0,
    steps   jsonb NOT NULL DEFAULT '[]'  -- [{"do": "read"}, {"do": "move", "arg": "<folder id>"}, …]
);
CREATE INDEX mail_quicksteps_user_idx ON mail_quicksteps (user_id, pos);
-- Outlook's «Готово» and «Ответить и удалить» are made for the user once (one who deletes them keeps none)
ALTER TABLE users ADD COLUMN mail_quicksteps_made boolean NOT NULL DEFAULT false;
