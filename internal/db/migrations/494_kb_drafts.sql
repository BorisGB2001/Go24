-- A draft of a page being edited in the knowledge base, as in Confluence: the editor saves the text by
-- itself, so a closed tab or a lost connection loses nothing, and «Сохранить» publishes it. The draft remembers the
-- version of the page it started from (base): if someone saved the page since, the editor warns before overwriting.
-- seen_at is the last sign of the open editor — «Сейчас редактирует» on the page.
CREATE TABLE kb_drafts (
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    page_id  uuid NOT NULL REFERENCES kb_pages_all(id) ON DELETE CASCADE,
    title    text NOT NULL,
    body     text NOT NULL,
    base     timestamptz NOT NULL, -- updated_at of the page the draft was written over
    saved_at timestamptz NOT NULL DEFAULT now(),
    seen_at  timestamptz,          -- NULL: the editor is closed
    PRIMARY KEY (user_id, page_id)
);
CREATE INDEX kb_drafts_page_idx ON kb_drafts (page_id) WHERE seen_at IS NOT NULL;
