-- CRM's «Права доступа» (GO24DEV-938): a role is given to employees or departments (with the ones
-- below). The new actions — add, export, import — and the rights on deals of a pipeline (keys "deal:<pipeline id>")
-- live in crm_roles.perms; a role saved before them reads add and import as its write level, export as its read one.
ALTER TABLE crm_role_members DROP CONSTRAINT crm_role_members_pkey;
ALTER TABLE crm_role_members ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE crm_role_members ADD COLUMN department_id uuid REFERENCES departments(id) ON DELETE CASCADE;
ALTER TABLE crm_role_members ADD CHECK ((user_id IS NULL) <> (department_id IS NULL));
CREATE UNIQUE INDEX crm_role_members_user_idx ON crm_role_members (role_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX crm_role_members_dept_idx ON crm_role_members (role_id, department_id) WHERE department_id IS NOT NULL;
