-- Mail as in Outlook: «Макет». The density of the letter list (roomy, cozy — the default, compact), the text size of
-- the list and the reading pane, whether a row shows a line of the letter's text, the sender's picture and the
-- names of the letter's files.
ALTER TABLE mail_settings
    ADD COLUMN density     text    NOT NULL DEFAULT 'cozy' CHECK (density IN ('roomy', 'cozy', 'compact')),
    ADD COLUMN text_size   text    NOT NULL DEFAULT 'medium' CHECK (text_size IN ('small', 'medium', 'large')),
    ADD COLUMN show_text   boolean NOT NULL DEFAULT true,
    ADD COLUMN show_pics   boolean NOT NULL DEFAULT true,
    ADD COLUMN show_files  boolean NOT NULL DEFAULT true;
