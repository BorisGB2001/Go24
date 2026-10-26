-- Flows (Потоки) tasks.flow: a team's queue of tasks. Tasks put into a flow go to its project with
-- the deadline of the flow's planned time; the flow picks the responsible:
--   manually — the moderator gets every task and hands it on;
--   queue    — the team in turn (round robin), skipping people away today;
--   himself  — the task waits with its creator, the team is notified, whoever takes it becomes the responsible.
-- The flow's people (administrator, moderator, team) see its tasks.
CREATE TABLE task_flows (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                  text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 200),
    description           text NOT NULL DEFAULT '',
    project_id            uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_id              uuid NOT NULL REFERENCES users(id),          -- администратор потока
    moderator_id          uuid REFERENCES users(id) ON DELETE SET NULL, -- manually: who hands tasks on (NULL = the owner)
    distribution          text NOT NULL DEFAULT 'manually' CHECK (distribution IN ('manually', 'queue', 'himself')),
    planned_minutes       integer NOT NULL DEFAULT 1440 CHECK (planned_minutes > 0), -- plannedCompletionTime
    template_id           uuid REFERENCES task_templates(id) ON DELETE SET NULL,     -- the form of the flow's tasks
    creators_all          boolean NOT NULL DEFAULT true,  -- everyone may put tasks in; otherwise task_flow_creators
    match_work_time       boolean NOT NULL DEFAULT true,
    allow_change_deadline boolean NOT NULL DEFAULT false,
    requires_control      boolean NOT NULL DEFAULT false,
    active                boolean NOT NULL DEFAULT true,
    queue_last            uuid REFERENCES users(id) ON DELETE SET NULL, -- queue: who got the last task
    created_by            uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at            timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE task_flow_members (
    flow_id uuid NOT NULL REFERENCES task_flows(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (flow_id, user_id)
);
CREATE INDEX task_flow_members_user_idx ON task_flow_members (user_id);

CREATE TABLE task_flow_creators (
    flow_id uuid NOT NULL REFERENCES task_flows(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (flow_id, user_id)
);

ALTER TABLE tasks_all ADD COLUMN flow_id uuid REFERENCES task_flows(id) ON DELETE SET NULL;
CREATE INDEX tasks_all_flow_idx ON tasks_all (flow_id) WHERE flow_id IS NOT NULL;
CREATE OR REPLACE VIEW tasks AS SELECT * FROM tasks_all WHERE deleted_at IS NULL; -- the new column (see 060)
