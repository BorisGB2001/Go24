-- Mail as in Outlook, step 4: «Отсортированные» and «Другие» of the inbox, and searches.
-- other: an inbox letter shown under «Другие» (newsletters, notifications, bulk mail) rather than «Отсортированные».
ALTER TABLE mail_messages ADD COLUMN other boolean NOT NULL DEFAULT false;
UPDATE mail_messages x SET other = true FROM mail_folders f WHERE f.id = x.folder_id AND f.role = 'inbox'
    AND (x.from_addr ~* '^(no-?reply|do-?not-?reply|notifications?|news(letter)?|mailer-daemon|bounce|marketing|promo|digest|alerts?)([+.-].*)?@'
         OR x.body ILIKE '%unsubscribe%' OR x.body ILIKE '%отписаться%');

-- What go24 learnt of a sender for the user: score > 0 — «Отсортированные», < 0 — «Другие» (moving a letter, replying,
-- flagging, deleting unread change it); always — the user's rule «Всегда перемещать» ('' — none).
CREATE TABLE mail_focus (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    sender  text NOT NULL,  -- the address, lower case
    score   int NOT NULL DEFAULT 0,
    always  text NOT NULL DEFAULT '' CHECK (always IN ('', 'focused', 'other')),
    PRIMARY KEY (user_id, sender)
);

-- Searches: the recent ones (name '' — the suggestions) and the saved ones, shown as search folders.
CREATE TABLE mail_searches (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    q       text NOT NULL CHECK (length(q) BETWEEN 1 AND 500),
    used_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX mail_searches_recent_idx ON mail_searches (user_id, q) WHERE name = '';
CREATE UNIQUE INDEX mail_searches_saved_idx ON mail_searches (user_id, lower(name)) WHERE name <> '';

-- the inbox shows «Отсортированные» and «Другие» unless the user turned them off
ALTER TABLE mail_settings ADD COLUMN focused boolean NOT NULL DEFAULT true;
