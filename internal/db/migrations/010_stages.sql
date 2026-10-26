-- Kanban stages of a project group stages (b_tasks_stages): every project has its own
-- columns. A task without a stage sits in the first one. The "finish" stage holds finished tasks:
-- completing a task moves it there, and dropping a task there completes it.
CREATE TABLE project_stages (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    code       text CHECK (code IN ('new', 'work', 'finish')), -- built-in stage: title comes from the locale
    title      text NOT NULL DEFAULT '',                        -- custom title (or a renamed built-in stage)
    color      text NOT NULL DEFAULT '00C4FB' CHECK (color ~ '^[0-9A-F]{6}$'),
    sort       integer NOT NULL DEFAULT 0,
    legacy_id  integer,                                         -- b_tasks_stages.ID, filled by the importer
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX project_stages_project_idx ON project_stages (project_id, sort);
CREATE UNIQUE INDEX project_stages_finish_idx ON project_stages (project_id) WHERE code = 'finish';

ALTER TABLE tasks ADD COLUMN stage_id uuid REFERENCES project_stages(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN stage_sort integer NOT NULL DEFAULT 0;
CREATE INDEX tasks_stage_idx ON tasks (stage_id);
