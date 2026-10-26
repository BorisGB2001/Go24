-- «Импорт» of a calendar file (.ics): the UID a meeting came with, so that importing the file again
-- changes the meetings it made instead of making them twice.
ALTER TABLE events ADD COLUMN import_uid text;
CREATE UNIQUE INDEX events_import_uid_idx ON events (owner_id, import_uid) WHERE import_uid IS NOT NULL;
