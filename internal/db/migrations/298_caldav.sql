-- Two-way sync over CalDAV (GO24DEV-429): the name a calendar program gave a meeting it put into
-- go24 (its resource in the collection; the others are named by their ID), so that the program finds it again there.
ALTER TABLE events ADD COLUMN dav_name text;
CREATE UNIQUE INDEX events_dav_name_idx ON events (owner_id, dav_name) WHERE dav_name IS NOT NULL;
