-- Mail as in Outlook: reactions to a colleague's letter (👍 ❤️ 🎉 😆 😮 😢). A reaction does not go by mail: it is
-- kept by the letter's Message-ID (mail_messages.message_id), so every employee who has the letter sees it. One
-- reaction of a person per letter.
CREATE TABLE mail_reactions (
    message_id text        NOT NULL CHECK (message_id <> ''),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('like', 'heart', 'party', 'laugh', 'wow', 'sad')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, user_id)
);
