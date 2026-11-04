-- «Открытые линии»: the channel MAX (GO24DEV-849) — the company's bot in the MAX messenger (the MAX
-- platform for partners) is a way into the line, as the Telegram bot is. max_secret — the bot's token sealed with go24's
-- own secret; max_name — the bot's name in MAX (one line per bot); max_next — the marker of the events not read yet;
-- max_error — what went wrong the last time.
ALTER TABLE ol_widgets ADD COLUMN max_secret bytea,
    ADD COLUMN max_name text NOT NULL DEFAULT '',
    ADD COLUMN max_next bigint NOT NULL DEFAULT 0,
    ADD COLUMN max_error text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ol_widgets_max_idx ON ol_widgets (lower(max_name)) WHERE max_name <> '';

-- a dialogue from MAX: the chat, the client's user id there, the last message sent there
ALTER TABLE ol_sessions DROP CONSTRAINT ol_sessions_channel_check,
    ADD CONSTRAINT ol_sessions_channel_check CHECK (channel IN ('widget', 'telegram', 'max')),
    ADD COLUMN max_chat bigint,
    ADD COLUMN max_user text NOT NULL DEFAULT '',
    ADD COLUMN max_sent bigint NOT NULL DEFAULT 0;
CREATE INDEX ol_sessions_max_idx ON ol_sessions (widget_id) WHERE channel = 'max';
