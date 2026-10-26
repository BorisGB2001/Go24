-- Access to the dashboards of «BI-аналитика»: a dashboard is a draft (only those who may change it see
-- it) or published; it may be in a group of dashboards; «Права доступа» of a dashboard or of its group open it to
-- the employees, departments (with their sub-departments) or everyone listed — to view or to change. A dashboard
-- without lines of its own or of its group is seen by every employee; its author and the administrators always
-- change it. The dashboards made before stay published.
CREATE TABLE bi_groups (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title      text NOT NULL,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE bi_dashboards
    ADD COLUMN status text NOT NULL DEFAULT 'published' CHECK (status IN ('draft', 'published')),
    ADD COLUMN group_id uuid REFERENCES bi_groups(id) ON DELETE SET NULL;
ALTER TABLE bi_dashboards ALTER COLUMN status SET DEFAULT 'draft';

CREATE TABLE bi_rights (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    dashboard_id  uuid REFERENCES bi_dashboards(id) ON DELETE CASCADE,
    group_id      uuid REFERENCES bi_groups(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    access        text NOT NULL CHECK (access IN ('view', 'edit')),
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((dashboard_id IS NULL) <> (group_id IS NULL)),
    CHECK (user_id IS NULL OR department_id IS NULL)
);
CREATE UNIQUE INDEX bi_rights_whom_idx ON bi_rights
    (coalesce(dashboard_id, group_id), coalesce(user_id, department_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- bi_access is the viewer's access to the dashboard by the lines of it and of its group: NULL — there are none,
-- 'none' — it is not open to the viewer, else the best of the lines that are theirs.
CREATE FUNCTION bi_access(dash uuid, grp uuid, viewer uuid) RETURNS text LANGUAGE sql STABLE AS $$
    WITH RECURSIVE dep AS (
        SELECT department_id AS id FROM user_departments WHERE user_id = viewer
        UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
    SELECT CASE WHEN count(*) = 0 THEN NULL
        ELSE coalesce((ARRAY['view', 'edit'])[max(CASE WHEN r.user_id = viewer OR r.department_id IN (SELECT id FROM dep)
                OR (r.user_id IS NULL AND r.department_id IS NULL)
            THEN array_position(ARRAY['view', 'edit'], r.access) END)], 'none') END
    FROM bi_rights r WHERE r.dashboard_id = dash OR (grp IS NOT NULL AND r.group_id = grp)
$$;
