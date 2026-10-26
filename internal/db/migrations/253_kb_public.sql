-- «Публикация» of the knowledge base: a base or a page (with its subpages) opens by an outside link
-- /kbp/{token} for anyone, without signing in; reading only. A link per base or page; switched off — deleted.
CREATE TABLE kb_public_links (
    token      text PRIMARY KEY,
    space_id   uuid NOT NULL REFERENCES kb_spaces_all(id) ON DELETE CASCADE,
    page_id    uuid REFERENCES kb_pages_all(id) ON DELETE CASCADE, -- NULL: the whole base
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX kb_public_links_what_idx ON kb_public_links
    (space_id, coalesce(page_id, '00000000-0000-0000-0000-000000000000'::uuid));
