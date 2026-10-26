-- «Настройки → Администраторы», as «Права доступа → Администраторы» (GO24DEV-537): administrators made in
-- go24 besides the members of the Authentik groups GO24_ADMIN_GROUPS; admin_by — who made them, admin_at — when.
ALTER TABLE users
    ADD COLUMN admin    boolean NOT NULL DEFAULT false,
    ADD COLUMN admin_by uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN admin_at timestamptz;
