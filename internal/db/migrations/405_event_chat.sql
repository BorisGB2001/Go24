-- «Чат встречи» (GO24DEV-663): the messenger's group chat of the meeting's people, made on first use.
ALTER TABLE events ADD COLUMN chat_room text NOT NULL DEFAULT '';
