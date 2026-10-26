-- A letter shared «Создать → Сообщение в Ленте / Чат» of a letter (and Outlook's «Поделиться в
-- Teams»): the feed's post or the chat it went to, shown on the letter.
CREATE TABLE mail_letter_shares (
    id         bigserial PRIMARY KEY,
    message_id uuid NOT NULL REFERENCES mail_messages(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('post', 'chat')),
    target     text NOT NULL, -- the post's id or the chat's room
    title      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (message_id, kind, target)
);
