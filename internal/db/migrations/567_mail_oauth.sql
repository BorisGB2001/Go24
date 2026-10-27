-- A mailbox connected by signing in to its service (Google, Microsoft, Яндекс) as in Outlook: go24 signs in
-- to IMAP and SMTP with OAuth2 (XOAUTH2); secret then keeps the token (refreshed by go24), not a password.
ALTER TABLE mailboxes ADD COLUMN oauth text NOT NULL DEFAULT '' CHECK (oauth IN ('', 'google', 'microsoft', 'yandex'));
