-- «Коллабы», step 2: guests are invited (the portal «Пригласить в коллаб»): by a link anyone may open until it expires
-- or is revoked, or personally — by e-mail or a phone number, a link for one person. Whoever opens it signs up in
-- Authentik (the guests' flow, the group go24-guests) or signs in, and becomes a member of the collab; a person new
-- to go24 who comes so is a guest.
CREATE TABLE collab_invites (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    token      text NOT NULL UNIQUE,                 -- the secret of the link /join/{token}
    kind       text NOT NULL CHECK (kind IN ('link', 'email', 'phone')),
    email      text NOT NULL DEFAULT '',
    phone      text NOT NULL DEFAULT '',
    name       text NOT NULL DEFAULT '',             -- whom a personal invitation is for
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    uses       int NOT NULL DEFAULT 0,               -- people who joined by it (a personal one — at most one)
    used_by    uuid REFERENCES users(id) ON DELETE SET NULL, -- the last of them
    used_at    timestamptz
);
CREATE INDEX collab_invites_project_idx ON collab_invites (project_id);
