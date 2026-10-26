-- Open lines (GO24DEV-717): the chat bot «Интерактивное меню» — the line's bot answers a new dialogue
-- first with its greeting and a menu: an item answers with a text, opens a submenu, passes the client to the operators or
-- closes the dialogue; bot_other — what any other text does: «menu» (the menu again) or «queue» (to the operators).
ALTER TABLE ol_widgets
    ADD COLUMN bot_on bool NOT NULL DEFAULT false,
    ADD COLUMN bot_text text NOT NULL DEFAULT '',
    ADD COLUMN bot_menu jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN bot_other text NOT NULL DEFAULT 'menu';

-- where the client is in the bot's menu ('' — its top, '2.1' — the submenu of item 1 of item 2); NULL — not with the bot
ALTER TABLE ol_sessions ADD COLUMN bot_node text;

-- the bot's message: the menu's items as buttons of the widget and of Telegram
ALTER TABLE ol_messages ADD COLUMN buttons text[] NOT NULL DEFAULT '{}';
ALTER TABLE ol_messages DROP CONSTRAINT ol_messages_kind_check;
ALTER TABLE ol_messages ADD CONSTRAINT ol_messages_kind_check
    CHECK (kind IN ('', 'welcome', 'offline', 'noanswer', 'close', 'note', 'hidden', 'form', 'form_done', 'bot'));
