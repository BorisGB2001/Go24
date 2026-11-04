-- «Назначить политику», as Outlook's retention policies: a folder's policy (folder.retention: '' — none) and a
-- letter's own one (message.retention: '' — its folder's), such as del_1m — to «Удалённые» a month after it came,
-- arch_1y — to «Архив» after a year, never — kept (mail.Policies).
ALTER TABLE mail_folders ADD COLUMN retention text NOT NULL DEFAULT '';
ALTER TABLE mail_messages ADD COLUMN retention text NOT NULL DEFAULT '';
CREATE INDEX mail_folders_retention ON mail_folders (id) WHERE retention <> '';
CREATE INDEX mail_messages_retention ON mail_messages (folder_id) WHERE retention <> '';
