-- «Сайты»: the trash, the history of changes and the rights.
--
-- «Корзина»: a deleted site (with its pages) or page goes to the trash and comes back as it was, or is deleted for
-- good — by hand or after the days of the company setting of the trash (task_trash_days, as tasks, the Drive and the
-- knowledge base). As there (189), the tables become sites_all and site_pages_all, and sites / site_pages are views of
-- what is not in the trash; the addresses and the domain are unique among the live ones only.
ALTER TABLE sites RENAME TO sites_all;
ALTER TABLE sites_all
    ADD COLUMN deleted_at timestamptz,
    ADD COLUMN deleted_by uuid REFERENCES users(id) ON DELETE SET NULL,
    -- «Права доступа»: the role of every employee (0 — no access, 1 — view, 2 — edit, 3 — publish, 4 — full);
    -- people and departments may be given more in site_access
    ADD COLUMN everyone   smallint NOT NULL DEFAULT 3 CHECK (everyone BETWEEN 0 AND 4);
ALTER TABLE sites_all DROP CONSTRAINT sites_slug_key, DROP CONSTRAINT sites_domain_key;
CREATE UNIQUE INDEX sites_slug_idx ON sites_all (slug) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX sites_domain_idx ON sites_all (domain) WHERE deleted_at IS NULL;
CREATE VIEW sites AS SELECT * FROM sites_all WHERE deleted_at IS NULL;

ALTER TABLE site_pages RENAME TO site_pages_all;
ALTER TABLE site_pages_all
    ADD COLUMN deleted_at timestamptz,
    ADD COLUMN deleted_by uuid REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE site_pages_all DROP CONSTRAINT site_pages_site_id_slug_key;
CREATE UNIQUE INDEX site_pages_slug_idx ON site_pages_all (site_id, slug) WHERE deleted_at IS NULL;
CREATE INDEX site_pages_trash_idx ON site_pages_all (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE VIEW site_pages AS SELECT * FROM site_pages_all WHERE deleted_at IS NULL;

-- «История изменений»: the blocks of a page (or of the header or the footer of its site, page_id NULL) after each
-- change — what was done (act: add, save, up, down, hide, delete, move, revert; initial — as it was before its
-- history began) to a block of which type, by whom and when; any of them comes back.
CREATE TABLE site_revisions (
    id         bigserial PRIMARY KEY,
    site_id    uuid NOT NULL REFERENCES sites_all(id) ON DELETE CASCADE,
    page_id    uuid REFERENCES site_pages_all(id) ON DELETE CASCADE,
    area       text NOT NULL DEFAULT '',
    act        text NOT NULL,
    block_type text NOT NULL DEFAULT '',
    blocks     jsonb NOT NULL,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX site_revisions_page_idx ON site_revisions (site_id, page_id, area, id DESC);

-- The roles people and departments (with their sub-departments) have on a site; the creator and administrators have
-- full access, everyone else the highest of their roles and the site's «everyone».
CREATE TABLE site_access (
    site_id       uuid NOT NULL REFERENCES sites_all(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    role          smallint NOT NULL CHECK (role BETWEEN 1 AND 4),
    CHECK (num_nonnulls(user_id, department_id) = 1)
);
CREATE UNIQUE INDEX site_access_user_idx ON site_access (site_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX site_access_dept_idx ON site_access (site_id, department_id) WHERE department_id IS NOT NULL;
