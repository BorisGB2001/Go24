-- «Права доступа» of a folder of the company's drive or of a project's «Настроить права»: a folder with
-- rights of its own is open only to those listed — an employee, a department (with its sub-departments) or everyone
-- (both NULL) — for reading, editing or full access; a folder without them has the rights of the one above it.
CREATE TABLE drive_folder_rights (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    folder_id     uuid NOT NULL REFERENCES drive_folders(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    access        text NOT NULL CHECK (access IN ('read', 'edit', 'full')),
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (user_id IS NULL OR department_id IS NULL)
);
CREATE UNIQUE INDEX drive_folder_rights_whom_idx ON drive_folder_rights
    (folder_id, coalesce(user_id, department_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- drive_folder_access is the access of the viewer to the folder: NULL — no folder from it up has rights of its own
-- (the drive's own rules), 'none' — one of them is not open to the viewer, else that of the nearest one. A project's
-- owner and managers have every folder of its drive; a NULL viewer (an administrator) — every folder. On «Мой диск»
-- (a folder moved there) the rights are not looked at.
CREATE FUNCTION drive_folder_access(folder uuid, viewer uuid) RETURNS text LANGUAGE sql STABLE AS $$
    WITH RECURSIVE up AS (
        SELECT id, parent_id, drive, owner_id, 1 AS depth FROM drive_folders WHERE id = folder
        UNION ALL SELECT f.id, f.parent_id, f.drive, f.owner_id, up.depth + 1 FROM drive_folders f JOIN up ON f.id = up.parent_id
        WHERE up.depth < 50),
    dep AS (
        SELECT department_id AS id FROM user_departments WHERE user_id = viewer
        UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL),
    ranked AS (
        SELECT up.depth, max(CASE WHEN r.user_id = viewer OR r.department_id IN (SELECT id FROM dep)
                OR (r.user_id IS NULL AND r.department_id IS NULL)
            THEN array_position(ARRAY['read', 'edit', 'full'], r.access) END) AS best
        FROM up JOIN drive_folder_rights r ON r.folder_id = up.id WHERE up.drive <> 'user' GROUP BY up.depth)
    SELECT CASE
        WHEN viewer IS NULL OR NOT EXISTS (SELECT 1 FROM ranked) THEN NULL
        WHEN EXISTS (SELECT 1 FROM up JOIN project_members m ON up.depth = 1 AND up.drive = 'project'
            AND m.project_id = up.owner_id AND m.user_id = viewer AND m.role IN ('owner', 'manager')) THEN NULL
        WHEN EXISTS (SELECT 1 FROM ranked WHERE best IS NULL) THEN 'none'
        ELSE (ARRAY['read', 'edit', 'full'])[(SELECT best FROM ranked ORDER BY depth LIMIT 1)]
    END
$$;
