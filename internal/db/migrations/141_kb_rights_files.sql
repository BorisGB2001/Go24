-- «Права доступа» of a knowledge base: a base with rights of its own is open only to those listed — an
-- employee, a department (with its sub-departments) or everyone (both NULL) — for reading, editing or full access;
-- a base without them keeps its rules (a company base — everyone writes, a project base — its members).
CREATE TABLE kb_space_rights (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id      uuid NOT NULL REFERENCES kb_spaces(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    access        text NOT NULL CHECK (access IN ('read', 'edit', 'full')),
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (user_id IS NULL OR department_id IS NULL)
);
CREATE UNIQUE INDEX kb_space_rights_whom_idx ON kb_space_rights
    (space_id, coalesce(user_id, department_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- kb_space_access is the viewer's access to the base by its rights: NULL — it has none (its own rules), 'none' — it
-- is not open to the viewer, else the best of the lines that are theirs.
CREATE FUNCTION kb_space_access(space uuid, viewer uuid) RETURNS text LANGUAGE sql STABLE AS $$
    WITH RECURSIVE dep AS (
        SELECT department_id AS id FROM user_departments WHERE user_id = viewer
        UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
    SELECT CASE WHEN count(*) = 0 THEN NULL
        ELSE coalesce((ARRAY['read', 'edit', 'full'])[max(CASE WHEN r.user_id = viewer OR r.department_id IN (SELECT id FROM dep)
                OR (r.user_id IS NULL AND r.department_id IS NULL)
            THEN array_position(ARRAY['read', 'edit', 'full'], r.access) END)], 'none') END
    FROM kb_space_rights r WHERE r.space_id = space
$$;

-- Pictures and files of the pages of a knowledge base: whoever sees the base may open them.
ALTER TABLE files ADD COLUMN kb_space_id uuid REFERENCES kb_spaces(id) ON DELETE SET NULL;
CREATE INDEX files_kb_space_idx ON files (kb_space_id) WHERE kb_space_id IS NOT NULL;
