-- Access rights of «Складской учёт» (the portal «Права доступа» of the inventory): roles give each kind of stock
-- document the actions read, write, conduct, cancel, delete; the warehouses the documents may use (none — all);
-- seeing the purchase prices; the section's settings. A person has all their roles' rights together; one without
-- roles has the default role's. Administrators have every right.
CREATE TABLE crm_stock_roles (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text UNIQUE,             -- a built-in role: its name comes from the locales (stockroles.role.<code>)
    name       text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    is_default boolean NOT NULL DEFAULT false,
    perms      jsonb NOT NULL DEFAULT '{}', -- {"docs": {"arrival": ["read", "write"], …}, "warehouses": [ids], "purchase_price": true, "settings": false}
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (code IS NOT NULL OR name <> '')
);
CREATE UNIQUE INDEX crm_stock_roles_default_idx ON crm_stock_roles (is_default) WHERE is_default;
CREATE TABLE crm_stock_role_members (
    role_id uuid NOT NULL REFERENCES crm_stock_roles(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, user_id)
);
-- everyone sees the documents, as before; the storekeepers (those responsible for a warehouse) keep them
INSERT INTO crm_stock_roles (code, is_default, perms) VALUES ('everyone', true,
    '{"docs": {"arrival": ["read"], "adjustment": ["read"], "moving": ["read"], "deduct": ["read"], "shipment": ["read"]}}');
INSERT INTO crm_stock_roles (code, perms) VALUES ('storekeeper',
    '{"docs": {"arrival": ["read", "write", "conduct", "cancel", "delete"], "adjustment": ["read", "write", "conduct", "cancel", "delete"],
      "moving": ["read", "write", "conduct", "cancel", "delete"], "deduct": ["read", "write", "conduct", "cancel", "delete"],
      "shipment": ["read", "write", "conduct", "cancel", "delete"]}, "purchase_price": true}');
INSERT INTO crm_stock_role_members (role_id, user_id)
    SELECT DISTINCT r.id, w.responsible_id FROM crm_stock_roles r, crm_warehouses w WHERE r.code = 'storekeeper' AND w.responsible_id IS NOT NULL;
