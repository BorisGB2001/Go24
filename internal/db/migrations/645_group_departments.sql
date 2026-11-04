-- Departments of a group or project (GO24DEV-833): the people of a department and its sub-departments
-- (with the heads and deputies) are its members by themselves, those who come into the department later too.
CREATE TABLE project_departments (
    project_id    uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    department_id uuid NOT NULL REFERENCES departments(id) ON DELETE CASCADE,
    added_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, department_id)
);

-- The members the departments brought: only they go out of the group on leaving the departments (those added by hand
-- stay).
CREATE TABLE project_department_people (
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, user_id)
);
