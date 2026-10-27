-- The widget's look and «Правила показа»: the corner of the button, the pages it shows on (all, only the
-- listed or all but the listed; «*» — any characters), a delay before it shows, «Автоприглашение» (an operator's
-- greeting pops up by the button after hello_delay seconds) and hiding the button out of the line's working hours.
ALTER TABLE ol_widgets
    ADD COLUMN position     text NOT NULL DEFAULT 'bottom-right'
        CHECK (position IN ('bottom-right', 'bottom-left', 'top-right', 'top-left')),
    ADD COLUMN pages_mode   text NOT NULL DEFAULT 'all' CHECK (pages_mode IN ('all', 'only', 'except')),
    ADD COLUMN pages        text[] NOT NULL DEFAULT '{}',
    ADD COLUMN show_delay   int NOT NULL DEFAULT 0 CHECK (show_delay BETWEEN 0 AND 600),
    ADD COLUMN hello_on     boolean NOT NULL DEFAULT false,
    ADD COLUMN hello_text   text NOT NULL DEFAULT '' CHECK (length(hello_text) <= 1000),
    ADD COLUMN hello_delay  int NOT NULL DEFAULT 15 CHECK (hello_delay BETWEEN 0 AND 600),
    ADD COLUMN hide_offline boolean NOT NULL DEFAULT false;
