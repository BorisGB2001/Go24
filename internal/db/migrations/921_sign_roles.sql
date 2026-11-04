-- «Подпись → Права доступа» (GO24DEV-969): roles give each right a level — docs (seeing the documents
-- others sent: own — none of them, dept — of the people of one's departments with the ones below, all), edit (reminding,
-- the deadline, cancelling, sending anew, the blocks of others' documents: the same levels), send (sending for
-- signing: none, all), templates (changing others' templates of sending: own, all), hr (HR documents to employees and
-- everyone's «Сейф»: none, all — HR officers have it anyway). Members are employees or departments (with the ones
-- below). A person has the highest level of their roles; one without roles has the default role's. Administrators have
-- every right.
CREATE TABLE sign_roles (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text UNIQUE,             -- a built-in role: its name comes from the locales (signroles.role.<code>)
    name       text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    is_default boolean NOT NULL DEFAULT false,
    perms      jsonb NOT NULL DEFAULT '{}', -- {"docs": "own", "edit": "own", "send": "all", "templates": "own", "hr": "none"}
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (code IS NOT NULL OR name <> '')
);
CREATE UNIQUE INDEX sign_roles_default_idx ON sign_roles (is_default) WHERE is_default;
CREATE TABLE sign_role_members (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    role_id       uuid NOT NULL REFERENCES sign_roles(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX sign_role_members_user_idx ON sign_role_members (role_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX sign_role_members_dept_idx ON sign_role_members (role_id, department_id) WHERE department_id IS NOT NULL;
-- the rights stay as they were: everyone sees and manages their own documents and sends for signing
INSERT INTO sign_roles (code, is_default, perms) VALUES ('everyone', true,
    '{"docs": "own", "edit": "own", "send": "all", "templates": "own", "hr": "none"}');
INSERT INTO sign_roles (code, perms) VALUES ('heads',
    '{"docs": "dept", "edit": "own", "send": "all", "templates": "own", "hr": "none"}');
