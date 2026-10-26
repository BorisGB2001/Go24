-- Access rights of the CRM (the portal «Права доступа»): roles give each kind of card a level for reading, changing
-- and deleting — none, own (responsible), department (the responsible works in one of my departments), all. A person
-- has the highest level of their roles; one without roles has the default role's.
CREATE TABLE crm_roles (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text UNIQUE,             -- a built-in role: its name comes from the locales (crm.role.<code>)
    name       text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    is_default boolean NOT NULL DEFAULT false,
    perms      jsonb NOT NULL DEFAULT '{}', -- {"deal": {"read": "all", "write": "own", "delete": "none"}, …}
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (code IS NOT NULL OR name <> '')
);
CREATE UNIQUE INDEX crm_roles_default_idx ON crm_roles (is_default) WHERE is_default;
CREATE TABLE crm_role_members (
    role_id uuid NOT NULL REFERENCES crm_roles(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, user_id)
);
-- the CRM stays open to everyone until an administrator says otherwise
INSERT INTO crm_roles (code, is_default, perms) VALUES ('everyone', true,
    '{"deal": {"read": "all", "write": "all", "delete": "own"}, "lead": {"read": "all", "write": "all", "delete": "own"},
      "contact": {"read": "all", "write": "all", "delete": "own"}, "company": {"read": "all", "write": "all", "delete": "own"}}');
