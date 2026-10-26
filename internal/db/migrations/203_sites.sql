-- «Сайты»: sites made of pages, each page a list of ready blocks (a cover, a text, a picture, …) kept as
-- JSON [{id, type, hidden, data}]. A published site with a published page opens without signing in at
-- /site/{slug}/{page}; publishing the site publishes all its pages.
CREATE TABLE sites (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    description text NOT NULL DEFAULT '',
    published   boolean NOT NULL DEFAULT false,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE site_pages (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id     uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    slug        text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    title       text NOT NULL,
    blocks      jsonb NOT NULL DEFAULT '[]',
    is_home     boolean NOT NULL DEFAULT false,
    published   boolean NOT NULL DEFAULT false,
    sort        int NOT NULL DEFAULT 0,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, slug)
);
CREATE UNIQUE INDEX site_pages_home_idx ON site_pages (site_id) WHERE is_home;

-- Pictures of the pages: open to everyone, as the pages themselves.
ALTER TABLE files ADD COLUMN site_id uuid REFERENCES sites(id) ON DELETE SET NULL;
CREATE INDEX files_site_idx ON files (site_id) WHERE site_id IS NOT NULL;
