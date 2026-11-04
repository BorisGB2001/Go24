-- «Возможности группы»: a group's tools (feed, tasks, calendar, drive, chat) are switched off one by one
-- and get names of the group's own.
ALTER TABLE projects ADD COLUMN tools_off text[] NOT NULL DEFAULT '{}',
    ADD COLUMN tool_names jsonb NOT NULL DEFAULT '{}';
