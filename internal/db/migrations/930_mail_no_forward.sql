-- Mail: «Не пересылать», as Outlook's «Разрешения → Не пересылать»: the people it comes to in go24 may answer it, not
-- forward, print or copy it; the author's own copies (sent, drafts) are not restricted.
ALTER TABLE mail_messages ADD COLUMN no_forward boolean NOT NULL DEFAULT false;
