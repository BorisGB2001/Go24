-- «Открытые линии»: the channel WhatsApp (GO24DEV-906) — the company's WhatsApp Business number (the
-- Cloud API of Meta) is a way into the line. wa_secret — the number's access token and the Meta application's secret
-- (it signs the webhook's calls) sealed with go24's own secret; wa_phone — the number's id in the Cloud API (one line per
-- number); wa_number, wa_name — the number and the business's name for «Write in WhatsApp»; wa_waba — the WhatsApp
-- Business account go24 subscribed to its webhook ('' — the administrators set the webhook in the application); wa_base
-- — go24's public address Meta calls and takes the operators' files from; wa_error — what went wrong the last time.
ALTER TABLE ol_widgets ADD COLUMN wa_secret bytea,
    ADD COLUMN wa_phone text,
    ADD COLUMN wa_number text NOT NULL DEFAULT '',
    ADD COLUMN wa_name text NOT NULL DEFAULT '',
    ADD COLUMN wa_waba text NOT NULL DEFAULT '',
    ADD COLUMN wa_base text NOT NULL DEFAULT '',
    ADD COLUMN wa_error text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ol_widgets_wa_idx ON ol_widgets (wa_phone) WHERE wa_phone IS NOT NULL;

-- a dialogue from WhatsApp: the client's WhatsApp id (their number), the last message sent there
ALTER TABLE ol_sessions DROP CONSTRAINT ol_sessions_channel_check,
    ADD CONSTRAINT ol_sessions_channel_check CHECK (channel IN ('widget', 'telegram', 'max', 'vk', 'viber', 'ok', 'avito', 'whatsapp')),
    ADD COLUMN wa_user text NOT NULL DEFAULT '',
    ADD COLUMN wa_sent bigint NOT NULL DEFAULT 0;
CREATE INDEX ol_sessions_wa_idx ON ol_sessions (widget_id) WHERE channel = 'whatsapp';
