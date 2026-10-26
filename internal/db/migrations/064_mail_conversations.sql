-- Mail as in Outlook, step 2: conversations (by Message-ID / In-Reply-To / References — the thread is the root
-- letter's Message-ID), the flag (to do by a date, then complete; \Flagged on the server), pinned on top, colour
-- categories of the user, «Отложить» (snooze: hidden till then, then back unread on top of the folder).
ALTER TABLE mail_messages
    ADD COLUMN message_id   text NOT NULL DEFAULT '',  -- without the angle brackets, lower case
    ADD COLUMN thread       text NOT NULL DEFAULT '',  -- the conversation: the first of References, else In-Reply-To, else message_id
    ADD COLUMN cc_addr      text NOT NULL DEFAULT '',
    ADD COLUMN flagged      boolean NOT NULL DEFAULT false,
    ADD COLUMN flag_due     date,
    ADD COLUMN flag_done    boolean NOT NULL DEFAULT false,
    ADD COLUMN pinned       boolean NOT NULL DEFAULT false,
    ADD COLUMN categories   uuid[] NOT NULL DEFAULT '{}',
    ADD COLUMN snooze_until timestamptz,
    ADD COLUMN woke_at      timestamptz;              -- back from snooze: the list sorts it by this time
CREATE INDEX mail_messages_thread_idx ON mail_messages (mailbox_id, thread) WHERE thread <> '';
CREATE INDEX mail_messages_snooze_idx ON mail_messages (snooze_until) WHERE snooze_until IS NOT NULL;

-- the user's categories (Outlook: a name and a colour); a letter keeps their ids
CREATE TABLE mail_categories (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    color   text NOT NULL CHECK (color IN ('red', 'orange', 'yellow', 'green', 'teal', 'blue', 'purple', 'gray')),
    UNIQUE (user_id, name)
);
-- Outlook's colour categories are made for the user once (one who deletes them all keeps none)
ALTER TABLE users ADD COLUMN mail_categories_made boolean NOT NULL DEFAULT false;
