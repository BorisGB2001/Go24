-- «Запуск бизнес-процесса» (GO24DEV-630): a process started by a step of another keeps it — its page links to it, and a
-- chain of such starts is limited.
ALTER TABLE bp_instances ADD COLUMN parent_id uuid REFERENCES bp_instances(id) ON DELETE SET NULL;
