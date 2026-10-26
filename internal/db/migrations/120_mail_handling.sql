-- Mail as in Outlook: «Обработка сообщений». A letter is marked read when it opens in the reading pane
-- (read_after 0), after so many seconds there, or never by itself (-1); a letter asking for a read receipt is
-- asked about, always answered or never; after a letter of the reading pane is moved or deleted, the next one
-- opens, the previous one, or the folder alone.
ALTER TABLE mail_settings
    ADD COLUMN read_after int  NOT NULL DEFAULT 0 CHECK (read_after BETWEEN -1 AND 60),
    ADD COLUMN receipts   text NOT NULL DEFAULT 'ask' CHECK (receipts IN ('ask', 'always', 'never')),
    ADD COLUMN after_move text NOT NULL DEFAULT 'next' CHECK (after_move IN ('next', 'prev', 'folder'));
