-- Gantt links of the four the portal kinds (task.dependence TYPE): finish → start, start → start, finish → finish,
-- start → finish.
ALTER TABLE task_links DROP CONSTRAINT IF EXISTS task_links_type_check;
ALTER TABLE task_links ADD CONSTRAINT task_links_type_check
    CHECK (type IN ('finish_start', 'start_start', 'finish_finish', 'start_finish'));
