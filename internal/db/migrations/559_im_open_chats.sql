-- Open group chats and channels of the messenger: any employee finds them and joins without an
-- invitation (in Matrix their join rule is "restricted" to the company space). go24 lists them from here.
CREATE TABLE im_open_chats (
    room_id    text PRIMARY KEY,
    opened_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    opened_at  timestamptz NOT NULL DEFAULT now()
);
