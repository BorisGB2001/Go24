-- Chats of a collab (GO24DEV-716), as «Чаты в проекте»: besides its main chat a collab has group chats —
-- made in it, or a group chat of one's own added to it. The chat stays a plain Matrix group chat; this says whose it is.
-- No foreign key: when the collab is deleted, matrixsync takes the guests out of its chats before the row goes.
CREATE TABLE collab_chats (
    room_id    text PRIMARY KEY,
    project_id uuid NOT NULL,
    added_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX collab_chats_project_idx ON collab_chats (project_id);
