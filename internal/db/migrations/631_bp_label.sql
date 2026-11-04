-- Business processes with statuses state-machine ones (GO24DEV-825): label is the status the step
-- «Установить статус» set last; the process's lists and its card show it.
ALTER TABLE bp_instances ADD COLUMN label text NOT NULL DEFAULT '';
