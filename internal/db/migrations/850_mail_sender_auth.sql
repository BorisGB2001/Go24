-- Sender checks of a letter, as Outlook's: what the receiving server's Authentication-Results said of the From
-- domain (sender_auth: '' — not checked, pass, fail — Outlook's «?»), and the domain it came «через» (sender_via)
-- when SPF or DKIM passed only for another one.
ALTER TABLE mail_messages ADD COLUMN sender_auth text NOT NULL DEFAULT '';
ALTER TABLE mail_messages ADD COLUMN sender_via text NOT NULL DEFAULT '';

-- «Сообщить о фишинге» and «Сообщить о нежелательной почте»: what employees reported, for the administrators
-- (Outlook's «Reported messages»); the letter itself goes to the company's reporting address (company_settings
-- mail_report_to) as an attachment.
CREATE TABLE mail_reports (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       text NOT NULL, -- phishing, junk
    from_addr  text NOT NULL DEFAULT '',
    subject    text NOT NULL DEFAULT '',
    message_id text NOT NULL DEFAULT '',
    sent_at    timestamptz,
    sent_to    text NOT NULL DEFAULT '', -- the reporting address the copy went to ('' — none set)
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mail_reports_at ON mail_reports (created_at DESC);
