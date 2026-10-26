-- Access rights of the tasks (the portal «Задачи → Права доступа»): roles give levels for viewing, changing and deleting
-- tasks — own (as a participant), sub (and the tasks of one's subordinates), all — whom one may make responsible
-- (sub: oneself and one's subordinates, all: anyone) and export and import (none, all). A person has the highest
-- level of their roles; one without roles has the default role's. Guests keep their own tasks only.
CREATE TABLE task_roles (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text UNIQUE,             -- a built-in role: its name comes from the locales (taskroles.role.<code>)
    name       text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    is_default boolean NOT NULL DEFAULT false,
    perms      jsonb NOT NULL DEFAULT '{}', -- {"view": "sub", "edit": "sub", "delete": "sub", "assign": "all", …}
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (code IS NOT NULL OR name <> '')
);
CREATE UNIQUE INDEX task_roles_default_idx ON task_roles (is_default) WHERE is_default;
CREATE TABLE task_role_members (
    role_id uuid NOT NULL REFERENCES task_roles(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, user_id)
);
CREATE INDEX task_role_members_user_idx ON task_role_members (user_id);
-- the rights stay as they were until an administrator says otherwise
INSERT INTO task_roles (code, is_default, perms) VALUES ('everyone', true,
    '{"view": "sub", "edit": "sub", "delete": "sub", "assign": "all", "export": "all", "import": "all"}');

-- task_perm is the person's level of a right: the highest of their roles (none < own < sub < all).
CREATE FUNCTION task_perm(u uuid, k text) RETURNS text LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN coalesce((SELECT guest FROM users WHERE id = u), false) THEN
        CASE WHEN k IN ('view', 'edit', 'delete') THEN 'own' ELSE 'none' END
    ELSE coalesce((SELECT l FROM (
        SELECT r.perms->>k AS l FROM task_roles r JOIN task_role_members m ON m.role_id = r.id WHERE m.user_id = u
        UNION ALL SELECT r.perms->>k FROM task_roles r
            WHERE r.is_default AND NOT EXISTS (SELECT 1 FROM task_role_members m WHERE m.user_id = u)) x
        ORDER BY array_position(ARRAY['none', 'own', 'sub', 'all'], l) DESC NULLS LAST LIMIT 1), 'none') END
$$;
