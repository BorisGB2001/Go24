-- Mail as in Outlook: «Очистить беседу» / «Очистить папку» (Clean Up) — letters whose whole text a later letter of
-- the conversation quotes go to «Удалённые» or «Архив». «Обработка сообщений → Очистка беседы»: where they go and
-- which ones stay (unread, with a category, with a flag, digitally signed — kept by default, as in Outlook).
ALTER TABLE mail_settings
    ADD COLUMN cleanup_target      text NOT NULL DEFAULT 'delete',
    ADD COLUMN cleanup_unread      bool NOT NULL DEFAULT true,
    ADD COLUMN cleanup_categorized bool NOT NULL DEFAULT true,
    ADD COLUMN cleanup_flagged     bool NOT NULL DEFAULT true,
    ADD COLUMN cleanup_signed      bool NOT NULL DEFAULT true;
