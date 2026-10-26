-- «Открытые линии»: the channel Telegram — the company's bot (made with @BotFather) is a way into the
-- line: a client's messages to it become dialogues, the operators' answers and the line's messages go back there.
-- tg_secret — the bot's token sealed with go24's own secret; tg_name — the bot's @name (one line per bot);
-- tg_next — the first update not read yet; tg_error — what went wrong the last time.
ALTER TABLE ol_widgets ADD COLUMN tg_secret bytea,
    ADD COLUMN tg_name text NOT NULL DEFAULT '',
    ADD COLUMN tg_next bigint NOT NULL DEFAULT 0,
    ADD COLUMN tg_error text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ol_widgets_tg_idx ON ol_widgets (lower(tg_name)) WHERE tg_name <> '';

-- a dialogue from Telegram: the chat, the client's @name there, the last message sent there
ALTER TABLE ol_sessions ADD COLUMN channel text NOT NULL DEFAULT 'widget' CHECK (channel IN ('widget', 'telegram')),
    ADD COLUMN tg_chat bigint,
    ADD COLUMN tg_user text NOT NULL DEFAULT '',
    ADD COLUMN tg_sent bigint NOT NULL DEFAULT 0;
CREATE INDEX ol_sessions_tg_idx ON ol_sessions (widget_id) WHERE channel = 'telegram';
