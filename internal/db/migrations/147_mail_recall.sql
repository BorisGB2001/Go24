-- «Отозвать сообщение» and «Отправить повторно», as Outlook's (GO24DEV-346): what came of recalling a sent letter (by its Message-ID) from each
-- of its people — recalled (the unread copy deleted), read (too late), missing (no copy found), external (not a
-- mailbox of go24), failed (the server refused).
CREATE TABLE mail_recalls (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message_id text NOT NULL,
    addr       text NOT NULL,
    status     text NOT NULL,
    at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, message_id, addr)
);

-- «Отправить повторно»: a draft of a sent letter sent again (its files go along, it answers nothing).
ALTER TABLE mail_drafts DROP CONSTRAINT mail_drafts_mode_check;
ALTER TABLE mail_drafts ADD CONSTRAINT mail_drafts_mode_check CHECK (mode IN ('', 'reply', 'all', 'fwd', 'fwdatt', 'resend'));
