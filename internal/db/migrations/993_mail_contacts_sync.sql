-- Mail as in Outlook: «Личные контакты» on the phone and in Outlook (GO24DEV-991). The address book is a CardDAV book
-- of its own beside the employees: a program creates, changes and deletes contacts in it. A contact keeps the program's
-- name of its resource (href), its UID and the card as the program sent it (card: the fields go24 has no place for —
-- addresses, photo, more phones — stay); a contact made in go24 has none of them, its card is made from its fields.
-- A phone's contact may have no address (a phone number only); several contacts may share one.
ALTER TABLE mail_contacts
    ADD COLUMN href       text CHECK (length(href) <= 300),
    ADD COLUMN uid        text CHECK (length(uid) <= 300),
    ADD COLUMN card       text NOT NULL DEFAULT '' CHECK (length(card) <= 1048576),
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
    DROP CONSTRAINT mail_contacts_email_check,
    ADD CONSTRAINT mail_contacts_email_check CHECK (length(email) <= 320);
DROP INDEX mail_contacts_user_email_idx;
CREATE INDEX mail_contacts_user_email_idx ON mail_contacts (user_id, lower(email)) WHERE email <> '';
CREATE UNIQUE INDEX mail_contacts_user_href_idx ON mail_contacts (user_id, href);
