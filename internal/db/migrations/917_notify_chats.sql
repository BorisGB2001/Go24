-- Chat messages in the outside messengers (Telegram…) pushes them to the phone: the new messages of a
-- person's chats go to their linked chats while they are not in go24; a reply there is posted into the go24 chat.
-- notify_chat_sync: where the person's chats were last read from (a Matrix sync position).
CREATE TABLE notify_chat_sync (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    since   text NOT NULL
);

-- notify_chat_sent: a message sent to a messenger's chat (its id there) and the go24 message it repeats, so a reply
-- to it finds the chat and the thread.
CREATE TABLE notify_chat_sent (
    channel    text NOT NULL,
    chat       text NOT NULL,
    msg        text NOT NULL,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    room       text NOT NULL,
    event      text NOT NULL,
    root       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel, chat, msg)
);
CREATE INDEX notify_chat_sent_old ON notify_chat_sent (created_at);
