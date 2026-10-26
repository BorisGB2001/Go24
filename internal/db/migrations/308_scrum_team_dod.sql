-- Scrum, step 2 (GO24DEV-590): the scrum team — «Владелец продукта» (by default the project's
-- owner) and «Скрам-мастер», who plan and run sprints with the project's moderators; «Definition of Done» —
-- task types of the scrum project with their DoD checklists, ticked in each task before it is completed (a type
-- may make that compulsory).
ALTER TABLE projects
    ADD COLUMN scrum_owner_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN scrum_master_id uuid REFERENCES users(id) ON DELETE SET NULL;

CREATE TABLE scrum_types (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    dod_required boolean NOT NULL DEFAULT false,
    sort         integer NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX scrum_types_project_idx ON scrum_types (project_id, sort);

CREATE TABLE scrum_dod_items (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type_id uuid NOT NULL REFERENCES scrum_types(id) ON DELETE CASCADE,
    title   text NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    sort    integer NOT NULL DEFAULT 0
);
CREATE INDEX scrum_dod_items_type_idx ON scrum_dod_items (type_id, sort);

-- the type of a task (NULL — the project's first type)
ALTER TABLE tasks_all ADD COLUMN scrum_type_id uuid REFERENCES scrum_types(id) ON DELETE SET NULL;
CREATE OR REPLACE VIEW tasks AS SELECT * FROM tasks_all WHERE deleted_at IS NULL; -- the new column (see 060)

-- the DoD items ticked in a task
CREATE TABLE task_dod (
    task_id    uuid NOT NULL REFERENCES tasks_all(id) ON DELETE CASCADE,
    item_id    uuid NOT NULL REFERENCES scrum_dod_items(id) ON DELETE CASCADE,
    checked_by uuid REFERENCES users(id) ON DELETE SET NULL,
    checked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, item_id)
);
