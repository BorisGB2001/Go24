-- The form's fields a process was started with (their kinds and options as then): a user field shows a name,
-- a bool «Да»/«Нет», whatever the template becomes later.
ALTER TABLE bp_instances ADD COLUMN fields jsonb NOT NULL DEFAULT '[]';
