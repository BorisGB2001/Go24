-- «Открытые линии»: the channel Odnoklassniki (GO24DEV-893) — the company's OK group (its bot, the Bot
-- API of ok.ru) is a way into the line, as the VK community and the Viber bot are. ok_secret — the group's bot token
-- sealed with go24's own secret; ok_group — the group's id in OK (one line per group); ok_name — its name; ok_link — its
-- page for «Write in OK»; ok_base — go24's public address OK calls (the webhook) and takes the operators' pictures from;
-- ok_error — what went wrong the last time.
ALTER TABLE ol_widgets ADD COLUMN ok_secret bytea,
    ADD COLUMN ok_group text,
    ADD COLUMN ok_name text NOT NULL DEFAULT '',
    ADD COLUMN ok_link text NOT NULL DEFAULT '',
    ADD COLUMN ok_base text NOT NULL DEFAULT '',
    ADD COLUMN ok_error text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ol_widgets_ok_idx ON ol_widgets (ok_group) WHERE ok_group IS NOT NULL;

-- a dialogue from OK: the client's chat with the group and their user id there, the last message sent there
ALTER TABLE ol_sessions DROP CONSTRAINT ol_sessions_channel_check,
    ADD CONSTRAINT ol_sessions_channel_check CHECK (channel IN ('widget', 'telegram', 'max', 'vk', 'viber', 'ok')),
    ADD COLUMN ok_chat text NOT NULL DEFAULT '',
    ADD COLUMN ok_user text NOT NULL DEFAULT '',
    ADD COLUMN ok_sent bigint NOT NULL DEFAULT 0;
CREATE INDEX ol_sessions_ok_idx ON ol_sessions (widget_id) WHERE channel = 'ok';
