-- Mail as in Outlook and the portal: «Адреса отправителя». sender_name — the name letters of the mailbox go with ('' —
-- the user's own); send_as — the other addresses the mailbox may send from (its aliases, "Имя <адрес>", comma-separated);
-- send_default — the address a new letter is from ('' — the mailbox's own).
ALTER TABLE mailboxes
    ADD COLUMN sender_name  text NOT NULL DEFAULT '',
    ADD COLUMN send_as      text NOT NULL DEFAULT '',
    ADD COLUMN send_default text NOT NULL DEFAULT '';
