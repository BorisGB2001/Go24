-- Mail as in Outlook: «Предлагаемые ответы» — short answers the AI offers under a letter received; a click opens
-- the reply with one. They are kept with the letter, so it opens again without a new call to the AI (an empty list:
-- the letter needs no answer). «Параметры → Создание и ответ → Показывать предлагаемые ответы» (on by default).
ALTER TABLE mail_settings ADD COLUMN suggest bool NOT NULL DEFAULT true;

CREATE TABLE mail_suggestions (
    message_id uuid        PRIMARY KEY REFERENCES mail_messages (id) ON DELETE CASCADE,
    replies    text[]      NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
