-- «Избранное», «Недавние» and «Следить» of the knowledge base, as in Confluence: a person stars a base or a
-- page, the main page of the knowledge base lists the starred and the pages they opened lately; who watches a page
-- (with its subpages) or a whole base is notified when a page there is changed, written or moved to the trash. The
-- author of a page watches it from the start.
CREATE TABLE kb_marks (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('fav', 'watch')),
    space_id   uuid NOT NULL REFERENCES kb_spaces_all(id) ON DELETE CASCADE,
    page_id    uuid REFERENCES kb_pages_all(id) ON DELETE CASCADE, -- NULL: the whole base
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX kb_marks_what_idx ON kb_marks
    (user_id, kind, space_id, coalesce(page_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX kb_marks_watch_idx ON kb_marks (space_id) WHERE kind = 'watch';

CREATE TABLE kb_views (
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    page_id   uuid NOT NULL REFERENCES kb_pages_all(id) ON DELETE CASCADE,
    viewed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, page_id)
);
CREATE INDEX kb_views_recent_idx ON kb_views (user_id, viewed_at DESC);

-- the authors of the pages already written watch them
INSERT INTO kb_marks (user_id, kind, space_id, page_id)
SELECT created_by, 'watch', space_id, id FROM kb_pages_all WHERE deleted_at IS NULL ON CONFLICT DO NOTHING;
