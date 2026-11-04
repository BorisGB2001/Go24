-- «Открытые линии»: the channel Viber (GO24DEV-884) — the company's Viber bot is a way into the line, as
-- the Telegram and MAX bots and the VK community are. viber_secret — the bot's token sealed with go24's own secret;
-- viber_uri — the bot's address in Viber (one line per bot); viber_name — its name; viber_base — go24's public address
-- Viber calls (the webhook) and takes the operators' files from; viber_error — what went wrong the last time.
ALTER TABLE ol_widgets ADD COLUMN viber_secret bytea,
    ADD COLUMN viber_uri text,
    ADD COLUMN viber_name text NOT NULL DEFAULT '',
    ADD COLUMN viber_base text NOT NULL DEFAULT '',
    ADD COLUMN viber_error text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ol_widgets_viber_idx ON ol_widgets (viber_uri) WHERE viber_uri IS NOT NULL;

-- a dialogue from Viber: the client's user id there (their chat with the bot), the last message sent there
ALTER TABLE ol_sessions DROP CONSTRAINT ol_sessions_channel_check,
    ADD CONSTRAINT ol_sessions_channel_check CHECK (channel IN ('widget', 'telegram', 'max', 'vk', 'viber')),
    ADD COLUMN viber_user text NOT NULL DEFAULT '',
    ADD COLUMN viber_sent bigint NOT NULL DEFAULT 0;
CREATE INDEX ol_sessions_viber_idx ON ol_sessions (widget_id) WHERE channel = 'viber';
