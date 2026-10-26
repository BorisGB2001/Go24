-- Mail as in Outlook, step 7: notifications of new letters, blocked and safe senders.

-- New letters of the inbox are announced (the bell, the desktop, the messenger as the notification settings say):
-- those of «Отсортированные» only, all of them, or none; with a sound; a pop-up on the desktop.
ALTER TABLE mail_settings
    ADD COLUMN notify         text NOT NULL DEFAULT 'focused' CHECK (notify IN ('off', 'focused', 'all')),
    ADD COLUMN notify_sound   boolean NOT NULL DEFAULT true,
    ADD COLUMN notify_desktop boolean NOT NULL DEFAULT true;

-- «Заблокированные отправители» (their letters go to «Нежелательная почта», those come and those to come) and
-- «Надёжные отправители» (never blocked, their pictures from elsewhere always load). sender: an address or '@' and a
-- domain, lower case; one sender is one or the other.
CREATE TABLE mail_senders (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    sender     text NOT NULL CHECK (length(sender) BETWEEN 2 AND 300),
    kind       text NOT NULL CHECK (kind IN ('blocked', 'safe')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, sender)
);
