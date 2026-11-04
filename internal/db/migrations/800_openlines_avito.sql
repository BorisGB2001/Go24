-- «Открытые линии»: the channel Avito (GO24DEV-901) — the company's Avito account (the Messenger API of
-- an Avito application) is a way into the line: buyers' messages about its ads become dialogues. avito_secret — the
-- application's client id and secret sealed with go24's own secret; avito_user — the account's id in Avito (one line per
-- account); avito_name, avito_link — its name and profile page for «Write on Avito»; avito_base — go24's public address
-- for the links to the operators' files; avito_since — when connected: older chats are not taken; avito_error — what
-- went wrong the last time.
ALTER TABLE ol_widgets ADD COLUMN avito_secret bytea,
    ADD COLUMN avito_user bigint,
    ADD COLUMN avito_name text NOT NULL DEFAULT '',
    ADD COLUMN avito_link text NOT NULL DEFAULT '',
    ADD COLUMN avito_base text NOT NULL DEFAULT '',
    ADD COLUMN avito_since bigint NOT NULL DEFAULT 0,
    ADD COLUMN avito_error text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ol_widgets_avito_idx ON ol_widgets (avito_user) WHERE avito_user IS NOT NULL;

-- a dialogue from Avito: the chat and the buyer's user id there, the last message sent there, the time of the last
-- buyer's message taken
ALTER TABLE ol_sessions DROP CONSTRAINT ol_sessions_channel_check,
    ADD CONSTRAINT ol_sessions_channel_check CHECK (channel IN ('widget', 'telegram', 'max', 'vk', 'viber', 'ok', 'avito')),
    ADD COLUMN avito_chat text NOT NULL DEFAULT '',
    ADD COLUMN avito_user text NOT NULL DEFAULT '',
    ADD COLUMN avito_sent bigint NOT NULL DEFAULT 0,
    ADD COLUMN avito_seen bigint NOT NULL DEFAULT 0;
CREATE INDEX ol_sessions_avito_idx ON ol_sessions (widget_id) WHERE channel = 'avito';
