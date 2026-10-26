-- Efficiency (Эффективность): a task may be left out of its responsible's efficiency.
-- Violations are not stored: overdue comes from the deadline and closing time, «returned for rework» from the status
-- history, so a moved deadline or a new status counts at once.
ALTER TABLE tasks_all ADD COLUMN no_efficiency boolean NOT NULL DEFAULT false;
CREATE OR REPLACE VIEW tasks AS SELECT * FROM tasks_all WHERE deleted_at IS NULL; -- the new column (see 060)
CREATE INDEX task_history_rework_idx ON task_history (created_at) WHERE field = 'status' AND old_value = 'awaiting_control';
