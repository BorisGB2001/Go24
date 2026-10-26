-- Notifications in outside messengers (GO24DEV-560): Telegram, MAX, Slack, Mattermost… Each messenger has the
-- company's bot, set by the administrators; an employee links their chat with the bot by a code and picks which
-- notifications go there.

-- The company's bot of a messenger. name — the bot's own (t.me/<name>), read from the messenger when the token is
-- saved; server — the messenger's address where it is the company's own (Mattermost); secret — the token sealed with
-- go24's own secret; next_update — the first message to the bot not read yet.
CREATE TABLE notify_bots (
    channel     text PRIMARY KEY,
    enabled     boolean NOT NULL DEFAULT false,
    name        text NOT NULL DEFAULT '',
    server      text NOT NULL DEFAULT '',
    secret      bytea,
    next_update bigint NOT NULL DEFAULT 0,
    updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- An employee's chat with a bot. Until it is linked, code is what the person sends the bot (chat ''); then chat is
-- where the bot writes, account — who it is there (@name), off — the kinds not sent there, last_id — the last
-- notification looked at (the ones before the link are not sent).
CREATE TABLE notify_links (
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel   text NOT NULL,
    code      text UNIQUE,
    code_at   timestamptz,
    chat      text NOT NULL DEFAULT '',
    account   text NOT NULL DEFAULT '',
    off       text[] NOT NULL DEFAULT '{}',
    last_id   bigint NOT NULL DEFAULT 0,
    linked_at timestamptz,
    PRIMARY KEY (user_id, channel)
);
CREATE INDEX notify_links_chat_idx ON notify_links (channel, chat) WHERE chat <> '';
