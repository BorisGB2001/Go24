-- Mail as in Outlook: «Перевести». «Параметры → Перевод»: translate a letter not in the user's language by itself
-- (always), offer it (ask — the default) or never; the language letters are translated into ('' — the language of
-- the interface); the languages not offered for translation. A letter's translation is kept per language, so it
-- opens again without a new call to the AI.
ALTER TABLE mail_settings
    ADD COLUMN translate_mode text   NOT NULL DEFAULT 'ask' CHECK (translate_mode IN ('ask', 'always', 'never')),
    ADD COLUMN translate_to   text   NOT NULL DEFAULT '',
    ADD COLUMN translate_skip text[] NOT NULL DEFAULT '{}';

CREATE TABLE mail_translations (
    message_id uuid        NOT NULL REFERENCES mail_messages (id) ON DELETE CASCADE,
    lang       text        NOT NULL,
    from_lang  text        NOT NULL DEFAULT '',
    subject    text        NOT NULL DEFAULT '',
    body       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, lang)
);
