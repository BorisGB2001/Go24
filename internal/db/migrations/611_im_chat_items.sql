-- The tasks and meetings made of a chat's messages («Создать задачу», «Создать встречу»), as the «Задачи» and
-- «Встречи» of the chat panel list them (GO24DEV-813). item_id: tasks.id or events.id; a deleted one (or one the
-- person may not see) is skipped on reading.
CREATE TABLE im_chat_items (
    room_id    text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('task', 'event')),
    item_id    uuid NOT NULL,
    event_id   text NOT NULL DEFAULT '', -- the message it was made of ("" — made from the panel)
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, kind, item_id)
);
CREATE INDEX im_chat_items_item_idx ON im_chat_items (item_id);
