-- «Открытые линии»: the channel VK (GO24DEV-863) — the company's VK community is a way into the line, as
-- the Telegram and MAX bots are. vk_secret — the community's access key sealed with go24's own secret; vk_group — the
-- community's id (one line per community); vk_name, vk_screen — its name and short address in VK; vk_server, vk_key,
-- vk_ts — the Bots Long Poll server, its key and the events read so far; vk_error — what went wrong the last time.
ALTER TABLE ol_widgets ADD COLUMN vk_secret bytea,
    ADD COLUMN vk_group bigint,
    ADD COLUMN vk_name text NOT NULL DEFAULT '',
    ADD COLUMN vk_screen text NOT NULL DEFAULT '',
    ADD COLUMN vk_server text NOT NULL DEFAULT '',
    ADD COLUMN vk_key text NOT NULL DEFAULT '',
    ADD COLUMN vk_ts text NOT NULL DEFAULT '',
    ADD COLUMN vk_error text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ol_widgets_vk_idx ON ol_widgets (vk_group) WHERE vk_group IS NOT NULL;

-- a dialogue from VK: the client's chat with the community (peer_id), the client's user id there, the last message sent there.
-- The migration was renumbered after the later channels' ones, so the check lists all the channels known by now.
ALTER TABLE ol_sessions DROP CONSTRAINT ol_sessions_channel_check,
    ADD CONSTRAINT ol_sessions_channel_check CHECK (channel IN ('widget', 'telegram', 'max', 'vk', 'viber', 'ok', 'avito', 'whatsapp')),
    ADD COLUMN vk_peer bigint,
    ADD COLUMN vk_user text NOT NULL DEFAULT '',
    ADD COLUMN vk_sent bigint NOT NULL DEFAULT 0;
CREATE INDEX ol_sessions_vk_idx ON ol_sessions (widget_id) WHERE channel = 'vk';
