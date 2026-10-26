-- «Пригласить сотрудника»: invitations sent from go24 (the sign-up itself is Authentik's, see authentik.Invite).
CREATE TABLE employee_invites (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email       text NOT NULL CHECK (length(email) BETWEEN 3 AND 254),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    link        text NOT NULL,
    expires_at  timestamptz NOT NULL,
    invited_by  uuid NOT NULL REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX employee_invites_email_idx ON employee_invites (lower(email));
