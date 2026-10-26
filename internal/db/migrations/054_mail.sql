-- «Почта»: an employee connects a mailbox (IMAP to read, SMTP to send); go24 fetches new letters of the
-- inbox in the background and keeps their text; a letter can become a CRM lead.
CREATE TABLE mailboxes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email      text NOT NULL CHECK (length(email) BETWEEN 3 AND 254),
    imap_host  text NOT NULL,
    imap_port  int NOT NULL CHECK (imap_port BETWEEN 1 AND 65535),
    smtp_host  text NOT NULL,
    smtp_port  int NOT NULL CHECK (smtp_port BETWEEN 1 AND 65535),
    tls        boolean NOT NULL DEFAULT true,  -- IMAPS/SMTPS or STARTTLS; off only for a test server
    login      text NOT NULL,
    secret     bytea NOT NULL,                  -- the password, sealed with go24's key (AES-GCM)
    last_uid   bigint NOT NULL DEFAULT 0,
    validity   bigint NOT NULL DEFAULT 0,       -- the inbox's UIDVALIDITY
    synced_at  timestamptz,
    error      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, email)
);
CREATE TABLE mail_messages (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    uid        bigint NOT NULL,
    folder     text NOT NULL DEFAULT 'INBOX', -- INBOX | Sent (sent from go24)
    from_addr  text NOT NULL DEFAULT '',
    from_name  text NOT NULL DEFAULT '',
    to_addr    text NOT NULL DEFAULT '',
    subject    text NOT NULL DEFAULT '',
    body       text NOT NULL DEFAULT '',        -- plain text (HTML letters are reduced to their text)
    sent_at    timestamptz NOT NULL,
    seen       boolean NOT NULL DEFAULT false,
    lead_id    uuid REFERENCES crm_leads(id) ON DELETE SET NULL,
    UNIQUE (mailbox_id, folder, uid)
);
CREATE INDEX mail_messages_list_idx ON mail_messages (mailbox_id, folder, sent_at DESC);
