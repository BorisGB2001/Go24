-- «Стартовый инструмент» (GO24DEV-727), as in «Проекты AI»: what a collab opens with from the list of
-- collabs, after it is made and by an invitation — its chat (as before), tasks, drive, calendar or feed.
ALTER TABLE projects
    ADD COLUMN start_tool text NOT NULL DEFAULT 'chat' CHECK (start_tool IN ('chat', 'tasks', 'drive', 'calendar', 'feed'));
