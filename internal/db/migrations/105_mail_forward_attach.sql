-- «Переслать как вложение», as Outlook's: a draft forwarding a letter as an .eml attachment (mode fwdatt).
ALTER TABLE mail_drafts DROP CONSTRAINT mail_drafts_mode_check;
ALTER TABLE mail_drafts ADD CONSTRAINT mail_drafts_mode_check CHECK (mode IN ('', 'reply', 'all', 'fwd', 'fwdatt'));
