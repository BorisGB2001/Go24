-- Mail as in Outlook, step 3: the editor and sending. Letters keep their HTML (cleaned) besides the text, their
-- importance, the blind copy (sent letters) and a read receipt asked for (incoming ones); pictures of a letter's
-- text are its files with a Content-ID.
ALTER TABLE mail_messages
    ADD COLUMN body_html    text NOT NULL DEFAULT '',  -- '' — a plain text letter
    ADD COLUMN bcc_addr     text NOT NULL DEFAULT '',
    ADD COLUMN importance   smallint NOT NULL DEFAULT 0 CHECK (importance BETWEEN -1 AND 1), -- low, normal, high
    ADD COLUMN receipt_to   text NOT NULL DEFAULT '',  -- Disposition-Notification-To of an incoming letter
    ADD COLUMN receipt_done boolean NOT NULL DEFAULT false; -- the receipt was sent or declined
ALTER TABLE files ADD COLUMN content_id text NOT NULL DEFAULT ''; -- a picture in a letter's text (cid:), not an attachment

-- A draft is a letter of the mailbox's «Черновики» go24 keeps itself (uid above the IMAP range); this is what the
-- editor needs of it besides the letter. send_at: it goes then — scheduled («Отправить позже») or the seconds
-- «Отменить» is offered after «Отправить»; sending — taken by a sender.
CREATE TABLE mail_drafts (
    id         uuid PRIMARY KEY REFERENCES mail_messages(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reply_to   text NOT NULL DEFAULT '',          -- the letter answered or forwarded
    mode       text NOT NULL DEFAULT '' CHECK (mode IN ('', 'reply', 'all', 'fwd')),
    refs       text[] NOT NULL DEFAULT '{}',      -- References of the answer, the conversation's root first
    receipt    boolean NOT NULL DEFAULT false,    -- ask for a read receipt
    delivery   boolean NOT NULL DEFAULT false,    -- ask for a delivery receipt
    send_at    timestamptz,
    scheduled  boolean NOT NULL DEFAULT false,    -- send_at is the user's time, not the undo delay
    sending    boolean NOT NULL DEFAULT false,
    error      text NOT NULL DEFAULT '',          -- why the last try failed
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mail_drafts_due_idx ON mail_drafts (send_at) WHERE send_at IS NOT NULL;

-- Signatures (Outlook: several, each mailbox has one for new letters and one for replies and forwards)
CREATE TABLE mail_signatures (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    html    text NOT NULL DEFAULT '',
    UNIQUE (user_id, name)
);
ALTER TABLE mailboxes
    ADD COLUMN sig_new   uuid REFERENCES mail_signatures(id) ON DELETE SET NULL,
    ADD COLUMN sig_reply uuid REFERENCES mail_signatures(id) ON DELETE SET NULL;

-- «Мои шаблоны»: the user's pieces of text put into a letter
CREATE TABLE mail_templates (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    html    text NOT NULL DEFAULT '',
    UNIQUE (user_id, name)
);

-- the user's mail settings: the seconds to undo sending (0 — off, up to 10 as in Outlook), the letter's font
CREATE TABLE mail_settings (
    user_id      uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    undo_seconds int NOT NULL DEFAULT 5 CHECK (undo_seconds BETWEEN 0 AND 10),
    font         text NOT NULL DEFAULT '',
    font_size    text NOT NULL DEFAULT ''
);
