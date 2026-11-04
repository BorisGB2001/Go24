-- «Тематика» and «Ключевые слова» of a portal group: the portal administrator keeps the list of subjects, a group
-- or project picks one; «Все группы» is filtered by the subject and searched by the keywords.
CREATE TABLE group_subjects (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    sort       integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX group_subjects_name_idx ON group_subjects (lower(name));

ALTER TABLE projects ADD COLUMN subject_id uuid REFERENCES group_subjects(id) ON DELETE SET NULL,
    ADD COLUMN keywords text[] NOT NULL DEFAULT '{}';
CREATE INDEX projects_keywords_idx ON projects USING gin (keywords);
