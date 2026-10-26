-- Re-runnable imports: comments remember their source id (tasks already have legacy_id).
ALTER TABLE task_comments ADD COLUMN legacy_id integer UNIQUE;
