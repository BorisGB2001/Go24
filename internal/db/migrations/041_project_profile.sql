-- Projects: an avatar, the type «проект» (with its dates) beside «группа» and «скрам», the archive
-- (the archived column has been there since 002).
ALTER TABLE projects ADD COLUMN photo_key text NOT NULL DEFAULT '',
                     ADD COLUMN photo_type text NOT NULL DEFAULT '',
                     ADD COLUMN date_start date,
                     ADD COLUMN date_finish date CHECK (date_finish >= date_start);
ALTER TABLE projects DROP CONSTRAINT projects_kind_check;
ALTER TABLE projects ADD CONSTRAINT projects_kind_check CHECK (kind IN ('group', 'project', 'scrum'));
