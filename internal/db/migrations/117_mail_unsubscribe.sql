-- «Отказаться от подписки», as in Outlook: how a newsletter lets one leave it (its List-Unsubscribe header and whether
-- List-Unsubscribe-Post offers one click, RFC 8058) and when the user left it.
ALTER TABLE mail_messages
    ADD COLUMN unsubscribe           text NOT NULL DEFAULT '',
    ADD COLUMN unsubscribe_one_click boolean NOT NULL DEFAULT false,
    ADD COLUMN unsubscribed_at       timestamptz;
