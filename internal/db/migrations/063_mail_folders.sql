-- Mail folders as in Outlook: every IMAP folder of a mailbox (Входящие, Черновики, Отправленные, Удалённые, Спам,
-- Архив by their special use or usual names, and the user's own), each fetched on its own (UIDVALIDITY, last UID).
-- A local folder is go24's own (letters sent before go24 put them on the server): the folder list never drops it.
CREATE TABLE mail_folders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    name       text NOT NULL,                  -- the IMAP name, e.g. INBOX or Archive/2026
    delim      text NOT NULL DEFAULT '/',      -- the hierarchy delimiter of the server
    role       text NOT NULL DEFAULT '' CHECK (role IN ('', 'inbox', 'drafts', 'sent', 'trash', 'junk', 'archive')),
    selectable boolean NOT NULL DEFAULT true,  -- \Noselect folders only hold others
    local      boolean NOT NULL DEFAULT false,
    last_uid   bigint NOT NULL DEFAULT 0,
    validity   bigint NOT NULL DEFAULT 0,
    UNIQUE (mailbox_id, name)
);

INSERT INTO mail_folders (mailbox_id, name, role, last_uid, validity) SELECT id, 'INBOX', 'inbox', last_uid, validity FROM mailboxes;
INSERT INTO mail_folders (mailbox_id, name, role, local) SELECT DISTINCT mailbox_id, 'Sent', 'sent', true FROM mail_messages WHERE folder = 'Sent';

ALTER TABLE mail_messages ADD COLUMN folder_id uuid REFERENCES mail_folders(id) ON DELETE CASCADE;
UPDATE mail_messages x SET folder_id = f.id FROM mail_folders f WHERE f.mailbox_id = x.mailbox_id AND f.name = x.folder;
DELETE FROM mail_messages WHERE folder_id IS NULL;
ALTER TABLE mail_messages ALTER COLUMN folder_id SET NOT NULL;
ALTER TABLE mail_messages DROP COLUMN folder; -- with its unique key and the list index
ALTER TABLE mail_messages ADD CONSTRAINT mail_messages_folder_uid_key UNIQUE (folder_id, uid);
CREATE INDEX mail_messages_list_idx ON mail_messages (folder_id, sent_at DESC);
ALTER TABLE mailboxes DROP COLUMN last_uid, DROP COLUMN validity;
