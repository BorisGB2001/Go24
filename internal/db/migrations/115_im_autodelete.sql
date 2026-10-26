-- Auto-delete of a chat's messages (GO24DEV-297): the setting itself is the room's go24.autodelete
-- state in Matrix; this is its index for the worker — the chat, how long a message lives, since when (only the
-- messages sent after it was turned on go), who turned it on (whose token deletes), how far it has deleted.
CREATE TABLE im_autodelete (
    room_id text PRIMARY KEY,
    ttl     integer NOT NULL CHECK (ttl > 0),
    since   timestamptz NOT NULL DEFAULT now(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    swept   timestamptz
);
