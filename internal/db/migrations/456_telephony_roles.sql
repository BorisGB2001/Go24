-- Telephony's access rights, as «Телефония → Права доступа» (GO24DEV-695): roles give each right a level —
-- calls (the log and the statistics: none, own, dept — one's departments with the ones below, all), records (listening
-- and the text: the same levels), outgoing (none, crm — inner numbers and CRM's clients only, all) and settings (none,
-- all). Members are employees or departments (with the ones below). A person has the highest level of their roles;
-- one without roles has the default role's. Administrators have every right.
CREATE TABLE telephony_roles (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text UNIQUE,             -- a built-in role: its name comes from the locales (telroles.role.<code>)
    name       text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    is_default boolean NOT NULL DEFAULT false,
    perms      jsonb NOT NULL DEFAULT '{}', -- {"calls": "own", "records": "own", "outgoing": "all", "settings": "none"}
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (code IS NOT NULL OR name <> '')
);
CREATE UNIQUE INDEX telephony_roles_default_idx ON telephony_roles (is_default) WHERE is_default;
CREATE TABLE telephony_role_members (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    role_id       uuid NOT NULL REFERENCES telephony_roles(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX telephony_role_members_user_idx ON telephony_role_members (role_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX telephony_role_members_dept_idx ON telephony_role_members (role_id, department_id) WHERE department_id IS NOT NULL;
-- the rights stay as they were: everyone sees their own calls and records and calls anywhere
INSERT INTO telephony_roles (code, is_default, perms) VALUES ('everyone', true,
    '{"calls": "own", "records": "own", "outgoing": "all", "settings": "none"}');
INSERT INTO telephony_roles (code, perms) VALUES ('heads',
    '{"calls": "dept", "records": "dept", "outgoing": "all", "settings": "none"}');
