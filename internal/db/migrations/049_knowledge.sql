-- «База знаний»: spaces (for everyone or a project) with a tree of pages; every edit is kept.
CREATE TABLE kb_spaces (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '',
    project_id  uuid REFERENCES projects(id) ON DELETE CASCADE, -- NULL: all employees
    created_by  uuid NOT NULL REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE kb_pages (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id   uuid NOT NULL REFERENCES kb_spaces(id) ON DELETE CASCADE,
    parent_id  uuid REFERENCES kb_pages(id) ON DELETE CASCADE,
    title      text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    body       text NOT NULL DEFAULT '' CHECK (length(body) <= 200000),
    sort       int NOT NULL DEFAULT 0,
    created_by uuid NOT NULL REFERENCES users(id),
    updated_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kb_pages_space_idx ON kb_pages (space_id, parent_id, sort);
CREATE TABLE kb_versions (
    id        bigserial PRIMARY KEY,
    page_id   uuid NOT NULL REFERENCES kb_pages(id) ON DELETE CASCADE,
    title     text NOT NULL,
    body      text NOT NULL,
    edited_by uuid NOT NULL REFERENCES users(id),
    edited_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kb_versions_page_idx ON kb_versions (page_id, id DESC);
