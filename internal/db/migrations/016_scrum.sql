-- Scrum scrum groups: a backlog, epics, story points, sprints (planned → active → completed).
-- A task of a scrum project is either in the backlog (sprint_id NULL) or in a sprint. The active sprint is
-- shown as a kanban on the project's stages.
ALTER TABLE projects ADD COLUMN kind text NOT NULL DEFAULT 'group' CHECK (kind IN ('group', 'scrum'));

CREATE TABLE epics (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '',
    color       text NOT NULL DEFAULT '69DAFF' CHECK (color ~ '^[0-9A-F]{6}$'),
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX epics_project_idx ON epics (project_id);

CREATE TABLE sprints (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    goal         text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'active', 'completed')),
    date_start   date,
    date_end     date,
    sort         integer NOT NULL DEFAULT 0,
    started_at   timestamptz,
    completed_at timestamptz,
    -- story points at start and done at completion, frozen for the statistics of completed sprints
    points_start numeric,
    points_done  numeric,
    legacy_id    integer,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sprints_project_idx ON sprints (project_id, status, sort);
CREATE UNIQUE INDEX sprints_one_active_idx ON sprints (project_id) WHERE status = 'active';

ALTER TABLE tasks
    ADD COLUMN sprint_id    uuid REFERENCES sprints(id) ON DELETE SET NULL,
    ADD COLUMN epic_id      uuid REFERENCES epics(id) ON DELETE SET NULL,
    ADD COLUMN story_points numeric CHECK (story_points >= 0 AND story_points <= 1000),
    ADD COLUMN backlog_sort integer NOT NULL DEFAULT 0;
CREATE INDEX tasks_sprint_idx ON tasks (sprint_id);
