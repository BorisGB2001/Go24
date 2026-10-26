-- Subtasks of a task template (the portal «Подзадачи шаблона»): a template under another one; a task made from the
-- base template gets a subtask from each of them. Only the base template repeats on its own.
ALTER TABLE task_templates ADD COLUMN parent_id uuid REFERENCES task_templates(id) ON DELETE CASCADE;
ALTER TABLE task_templates ADD CONSTRAINT task_templates_sub_once CHECK (parent_id IS NULL OR NOT replicate);
CREATE INDEX task_templates_parent_idx ON task_templates (parent_id) WHERE parent_id IS NOT NULL;
