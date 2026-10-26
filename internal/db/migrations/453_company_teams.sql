-- «Команды» of the portal company structure (GO24DEV-689): a team stands under a department, has a head, deputies
-- and members from any departments; a person may be in several teams.
CREATE TABLE teams (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    department_id uuid NOT NULL REFERENCES departments(id) ON DELETE RESTRICT,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description   text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX teams_department_idx ON teams (department_id);

CREATE TABLE team_members (
    team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role    text NOT NULL DEFAULT 'member' CHECK (role IN ('head', 'deputy', 'member')),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_members_user_idx ON team_members (user_id);
CREATE UNIQUE INDEX team_members_head_idx ON team_members (team_id) WHERE role = 'head';
