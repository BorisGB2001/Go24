-- Group chats of go24 linked to groups of an outside messenger (Telegram), as a bridge: the chat's new messages go to
-- the group, the group's messages come into the chat. A member who may rename the chat starts the link (a code to
-- send in the group with /link); one chat — one group, and the other way round.
CREATE TABLE notify_groups (
    room      text PRIMARY KEY,
    channel   text NOT NULL,
    chat      text NOT NULL DEFAULT '',
    title     text NOT NULL DEFAULT '',
    code      text,
    code_at   timestamptz,
    user_id   uuid REFERENCES users(id) ON DELETE SET NULL,
    linked_at timestamptz
);
CREATE UNIQUE INDEX notify_groups_chat ON notify_groups (channel, chat) WHERE chat <> '';
CREATE UNIQUE INDEX notify_groups_code ON notify_groups (channel, code) WHERE code IS NOT NULL;

-- notify_group_sync: where the bridge bot last read its chats from (a Matrix sync position; one row).
CREATE TABLE notify_group_sync (
    id    boolean PRIMARY KEY DEFAULT true CHECK (id),
    since text NOT NULL
);

-- notify_group_sent: a chat message sent to the group (its id there), so a reply to it there goes into its thread.
CREATE TABLE notify_group_sent (
    channel    text NOT NULL,
    chat       text NOT NULL,
    msg        text NOT NULL,
    room       text NOT NULL,
    event      text NOT NULL,
    root       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel, chat, msg)
);
CREATE INDEX notify_group_sent_old ON notify_group_sent (created_at);
