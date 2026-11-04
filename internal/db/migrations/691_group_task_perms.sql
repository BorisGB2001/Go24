-- «Права доступа» of a portal group to its tasks: who creates tasks in the group and who changes and deletes any of
-- them — the owner, the owner and the moderators (managers), or all members.
ALTER TABLE projects ADD COLUMN task_create text NOT NULL DEFAULT 'members' CHECK (task_create IN ('owner', 'managers', 'members')),
    ADD COLUMN task_edit text NOT NULL DEFAULT 'managers' CHECK (task_edit IN ('owner', 'managers', 'members')),
    ADD COLUMN task_delete text NOT NULL DEFAULT 'managers' CHECK (task_delete IN ('owner', 'managers', 'members'));
