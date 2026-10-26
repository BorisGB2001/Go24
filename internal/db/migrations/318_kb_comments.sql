-- Comments, «Нравится» and views of a page of the knowledge base, as in Confluence and the comments: under a
-- page people discuss it (a reply goes one step deep), like it; kb_views (315) already counts who opened it.
CREATE TABLE kb_comments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    page_id    uuid NOT NULL REFERENCES kb_pages_all(id) ON DELETE CASCADE,
    parent_id  uuid REFERENCES kb_comments(id) ON DELETE CASCADE, -- a reply to this comment
    author_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    body       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    edited_at  timestamptz
);
CREATE INDEX kb_comments_page_idx ON kb_comments (page_id, created_at);

CREATE TABLE kb_likes (
    page_id    uuid NOT NULL REFERENCES kb_pages_all(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (page_id, user_id)
);
