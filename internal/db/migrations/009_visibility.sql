-- Who sees a project's tasks:
--   members      — every project member (default, like a portal group / OpenProject project);
--   participants — only the task's creator, responsible, accomplices and auditors tasks without
--                  a group). Administrators in admin mode see everything.
ALTER TABLE projects ADD COLUMN visibility text NOT NULL DEFAULT 'members'
    CHECK (visibility IN ('members', 'participants'));

-- Tasks imported had no group there, so only their participants saw them.
UPDATE projects p SET visibility = 'participants'
WHERE EXISTS (SELECT 1 FROM tasks t WHERE t.project_id = p.id AND t.legacy_id IS NOT NULL);
