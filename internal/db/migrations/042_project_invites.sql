-- Invitations to a project (the portal groups): a person joins only after accepting. Who may invite is the
-- project's setting, as «Кто может приглашать в группу».
CREATE TABLE project_invites (
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('manager', 'member', 'viewer')),
    invited_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX project_invites_user_idx ON project_invites (user_id);

ALTER TABLE projects ADD COLUMN invite_perm text NOT NULL DEFAULT 'managers' CHECK (invite_perm IN ('owner', 'managers', 'members'));

-- the bell: «пригласил вас в проект»
ALTER TABLE notifications ADD COLUMN project_id uuid REFERENCES projects(id) ON DELETE CASCADE;
