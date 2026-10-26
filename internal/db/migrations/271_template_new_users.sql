-- «Шаблон для новых сотрудников» (the portal task template of type «for new users»): a task from the template is set
-- to each employee who comes to go24 after the template got the mark (new_users_since), the employee responsible.
-- Such a template does not repeat on schedule; task_template_hires remembers who got its task.
ALTER TABLE task_templates ADD COLUMN for_new_users boolean NOT NULL DEFAULT false,
    ADD COLUMN new_users_since timestamptz;
ALTER TABLE task_templates ADD CONSTRAINT task_templates_new_users CHECK (NOT for_new_users OR (NOT replicate AND parent_id IS NULL));

CREATE TABLE task_template_hires (
    template_id uuid NOT NULL REFERENCES task_templates(id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_id     uuid REFERENCES tasks_all(id) ON DELETE SET NULL, -- NULL: the task could not be made
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (template_id, user_id)
);
