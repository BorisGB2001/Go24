-- The rights of a collab «Права» of a collab (GO24DEV-403): who changes its settings, name, description
-- and avatar, and who writes in its chat.
ALTER TABLE projects
    ADD COLUMN edit_perm text NOT NULL DEFAULT 'managers' CHECK (edit_perm IN ('owner', 'managers')),          -- the owner | the owner and moderators
    ADD COLUMN chat_perm text NOT NULL DEFAULT 'members' CHECK (chat_perm IN ('members', 'employees', 'managers')); -- everyone | all but guests | the owner and moderators
