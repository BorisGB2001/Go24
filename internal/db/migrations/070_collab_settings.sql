-- «Коллабы», step 3: the settings of a collab (the portal «Настройки коллаба»): who may invite members (invite_perm,
-- as of projects) and who may invite guests; whether new members see the chat's history; what guests may do —
-- make tasks, upload files. And the collab's log: who made it, invited, joined, removed, changed, archived.
ALTER TABLE projects
    ADD COLUMN guest_invite_perm text NOT NULL DEFAULT 'managers' CHECK (guest_invite_perm IN ('owner', 'managers', 'members')),
    ADD COLUMN chat_history boolean NOT NULL DEFAULT true,  -- new members of the chat see what was said before them
    ADD COLUMN guests_tasks boolean NOT NULL DEFAULT true,  -- guests make tasks
    ADD COLUMN guests_files boolean NOT NULL DEFAULT true;  -- guests upload files (the drive, files of tasks)

-- what go24 last set on a room beyond its members (a collab's: history visibility, read-only in the archive)
ALTER TABLE matrix_rooms ADD COLUMN applied text NOT NULL DEFAULT '';

CREATE TABLE collab_log (
    id         bigserial PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    actor_id   uuid REFERENCES users(id) ON DELETE SET NULL,  -- NULL: go24 itself
    action     text NOT NULL,   -- created | member_added | joined | role_changed | member_removed | left | settings |
                                -- invite_created | invite_revoked | info | archived | restored
    target_id  uuid REFERENCES users(id) ON DELETE SET NULL,  -- the person it was about
    detail     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX collab_log_project_idx ON collab_log (project_id, created_at DESC);
