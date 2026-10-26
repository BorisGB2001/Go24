-- «Почта»: the reminder of a flagged letter, as Outlook's «Добавить напоминание» (GO24DEV-292): when it comes the
-- worker tells whoever set it (flag_remind_by: the owner of the mailbox or someone of a shared one, notification
-- mail_remind) and clears it
ALTER TABLE mail_messages ADD COLUMN flag_remind timestamptz, ADD COLUMN flag_remind_by uuid REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX mail_messages_flag_remind ON mail_messages (flag_remind) WHERE flag_remind IS NOT NULL;
