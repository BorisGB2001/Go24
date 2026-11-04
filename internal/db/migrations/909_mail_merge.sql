-- «Слияние» (Outlook's Mail Merge, GO24DEV-959): a draft sent to each of its «Кому» as a letter of its own, the fields
-- of the recipient put into its subject and text; merge_done — the addresses it already went to, so a send that
-- failed halfway goes on with the others.
ALTER TABLE mail_drafts
    ADD COLUMN merge      boolean NOT NULL DEFAULT false,
    ADD COLUMN merge_done text[] NOT NULL DEFAULT '{}';
