-- «Ожидать завершения задачи» of the «Задача» step: the process (or its parallel branch) stays on the step until
-- the task it made is completed or deleted.
ALTER TABLE bp_instances ADD COLUMN wait_task uuid;
ALTER TABLE bp_branches ADD COLUMN wait_task uuid;
CREATE INDEX bp_instances_wait_task_idx ON bp_instances (wait_task) WHERE wait_task IS NOT NULL;
