-- Messages a person keeps (GO24DEV-259): «Избранное» of the messenger, and «Напомнить» — when go24
-- is to remind them of the message (a notification with a link to it).
CREATE TABLE im_saved (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    room_id    text NOT NULL,
    event_id   text NOT NULL,
    saved      boolean NOT NULL DEFAULT false,
    remind_at  timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, event_id)
);
CREATE INDEX im_saved_remind ON im_saved (remind_at) WHERE remind_at IS NOT NULL;
CREATE INDEX im_saved_list ON im_saved (user_id, created_at DESC) WHERE saved;
