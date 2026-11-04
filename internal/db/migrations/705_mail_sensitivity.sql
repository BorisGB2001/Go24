-- Mail: «Конфиденциальность» of a letter, as Outlook's Sensitivity header: '' normal, personal, private,
-- confidential (Company-Confidential). A private letter of a shared mailbox is the owner's only.
ALTER TABLE mail_messages ADD COLUMN sensitivity text NOT NULL DEFAULT ''
    CHECK (sensitivity IN ('', 'personal', 'private', 'confidential'));
