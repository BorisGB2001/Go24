-- Business processes, «Остановить» (GO24DEV-386): a running process may be stopped with a reason
-- (in its history), and the administrators see all the processes.
ALTER TABLE bp_instances DROP CONSTRAINT bp_instances_status_check;
ALTER TABLE bp_instances ADD CONSTRAINT bp_instances_status_check CHECK (status IN ('running', 'done', 'rejected', 'failed', 'stopped'));
CREATE INDEX bp_instances_created_idx ON bp_instances (created_at DESC);
