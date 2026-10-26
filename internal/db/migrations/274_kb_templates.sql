-- «Шаблоны» of the knowledge base: a new base is made from a ready set of pages — one of go24's own
-- (in the code) or a base of the company saved as a template. The template keeps its pages as a tree and copies of
-- their pictures and files (files.kb_template_id); a base made from it gets copies of its own.
CREATE TABLE kb_templates (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    pages       jsonb NOT NULL, -- [{"title", "body", "children": […]}]
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE files ADD COLUMN kb_template_id uuid REFERENCES kb_templates(id) ON DELETE CASCADE;
CREATE INDEX files_kb_template_idx ON files (kb_template_id) WHERE kb_template_id IS NOT NULL;
