-- Drafts on the mail server, as Outlook keeps them (GO24DEV-249): a go24 draft is put into the server's «Черновики»
-- (pushed_at — the version put there; server_box, server_id — the mailbox and Message-ID of that copy). A copy no
-- longer wanted (the draft sent, deleted, moved to another mailbox) waits in mail_draft_drops for the next sync.
ALTER TABLE mail_drafts
    ADD COLUMN pushed_at  timestamptz,
    ADD COLUMN server_box uuid REFERENCES mailboxes(id) ON DELETE SET NULL,
    ADD COLUMN server_id  text NOT NULL DEFAULT '';

CREATE TABLE mail_draft_drops (
    mailbox_id uuid NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    message_id text NOT NULL,
    PRIMARY KEY (mailbox_id, message_id)
);

CREATE FUNCTION mail_draft_dropped() RETURNS trigger AS $$
BEGIN
    IF OLD.server_box IS NOT NULL AND OLD.server_id <> '' THEN
        -- not when the whole mailbox goes
        INSERT INTO mail_draft_drops (mailbox_id, message_id) SELECT OLD.server_box, OLD.server_id
            WHERE EXISTS (SELECT 1 FROM mailboxes WHERE id = OLD.server_box) ON CONFLICT DO NOTHING;
    END IF;
    RETURN OLD;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER mail_drafts_dropped AFTER DELETE ON mail_drafts FOR EACH ROW EXECUTE FUNCTION mail_draft_dropped();
