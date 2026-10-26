-- «Пригласить сотрудников» (GO24DEV-676): the department the person goes to on the first sign-in,
-- the Authentik invitation (to cancel it), cancelled invitations, the ones sent by e-mail.
ALTER TABLE employee_invites
    ADD COLUMN department_id uuid REFERENCES departments(id) ON DELETE SET NULL,
    ADD COLUMN authentik_id  text NOT NULL DEFAULT '',
    ADD COLUMN mailed        boolean NOT NULL DEFAULT false,
    ADD COLUMN revoked_at    timestamptz;
