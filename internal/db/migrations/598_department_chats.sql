-- «Чаты и каналы» of a department, as in the portal company structure (GO24DEV-804): the messenger's group chats and
-- channels tied to a department; its people (with the head and deputies, and with sub-departments when sub) come into
-- them by themselves and those who left it go out. owner_id: on whose behalf go24 adds and removes people.
CREATE TABLE department_chats (
    department_id uuid NOT NULL REFERENCES departments(id) ON DELETE CASCADE,
    room          text NOT NULL,
    name          text NOT NULL DEFAULT '', -- the chat's name when it was last seen
    channel       boolean NOT NULL DEFAULT false,
    sub           boolean NOT NULL DEFAULT false,
    owner_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (department_id, room)
);
CREATE INDEX department_chats_room_idx ON department_chats (room);

-- The people the department brought into the chat: only they go out of it on leaving the department (those added by
-- hand stay).
CREATE TABLE department_chat_people (
    department_id uuid NOT NULL,
    room          text NOT NULL,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (department_id, room, user_id),
    FOREIGN KEY (department_id, room) REFERENCES department_chats (department_id, room) ON DELETE CASCADE
);
