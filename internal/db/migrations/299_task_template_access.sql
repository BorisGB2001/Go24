-- «Права доступа» of a task template (GO24DEV-575): the people and the departments (with their
-- sub-departments) the author shares the template with — «Чтение» (see it, make tasks by it) or «Полный доступ»
-- (also change and delete it). Kept on base templates only: their subtask templates go with them.
CREATE TABLE task_template_access (
    template_id   uuid NOT NULL REFERENCES task_templates(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    full_access   boolean NOT NULL DEFAULT false,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX task_template_access_u ON task_template_access (template_id, coalesce(user_id, department_id));
