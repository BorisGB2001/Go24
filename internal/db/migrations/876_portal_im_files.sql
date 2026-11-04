-- REST compatible with the portal, the messenger part 6 (GO24DEV-937). A chat's colour (im.chat.updateColor) and the
-- external object it is bound to (ENTITY_TYPE and ENTITY_ID of im.chat.add and imbot.chat.add, found by
-- imbot.chat.get) keeps them; go24's own pages do not show them.
ALTER TABLE portal_chats ADD COLUMN color text NOT NULL DEFAULT '',
                      ADD COLUMN entity_type text NOT NULL DEFAULT '',
                      ADD COLUMN entity_id text NOT NULL DEFAULT '';
CREATE INDEX portal_chats_entity_idx ON portal_chats (entity_type, entity_id) WHERE entity_type <> '';

-- A file of the drive im.disk.file.commit sent into a chat: im.disk.file.delete takes its message back.
CREATE TABLE portal_chat_files (
    room_id  text NOT NULL,
    event_id text NOT NULL,
    file_id  uuid NOT NULL REFERENCES files ON DELETE CASCADE,
    PRIMARY KEY (room_id, file_id, event_id)
);
