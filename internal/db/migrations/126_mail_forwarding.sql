-- Mail as in Outlook: «Пересылка». Each mailbox may send every new letter of its inbox on to the addresses of
-- forward_to (comma-separated) while forward_on; forward_keep — a copy stays in the mailbox (otherwise the letter goes
-- to «Удалённые»).
ALTER TABLE mailboxes
    ADD COLUMN forward_on   boolean NOT NULL DEFAULT false,
    ADD COLUMN forward_to   text    NOT NULL DEFAULT '',
    ADD COLUMN forward_keep boolean NOT NULL DEFAULT true;
