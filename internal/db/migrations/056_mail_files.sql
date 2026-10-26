-- attachments of letters: files of a letter (deleted with it); only the mailbox's owner downloads them
ALTER TABLE files ADD COLUMN mail_id uuid REFERENCES mail_messages(id) ON DELETE CASCADE;
CREATE INDEX files_mail_idx ON files (mail_id) WHERE mail_id IS NOT NULL;
