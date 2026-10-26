-- Auto-delete of a collab chat's messages, as «Автоудаление сообщений» in the collab settings (GO24DEV-652):
-- how long a message lives, in seconds (0 — off), and who turned it on (the chat's line names them; their token
-- deletes first). The worker puts it into the room's go24.autodelete state and im_autodelete.
ALTER TABLE projects ADD COLUMN msg_ttl integer NOT NULL DEFAULT 0 CHECK (msg_ttl >= 0),
    ADD COLUMN msg_ttl_by uuid REFERENCES users(id) ON DELETE SET NULL;
