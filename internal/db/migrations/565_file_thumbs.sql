-- The thumbnails of files the tiles of «Диск» show, as the previews of the Disk: made the first time a file is
-- shown, for its state (object_key — a new version is a new key, its thumbnail is made anew). data NULL — no thumbnail
-- can be made of that state (not a picture, a broken file): not tried again.
CREATE TABLE file_thumbs (
    file_id    uuid PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    object_key text NOT NULL,
    data       bytea,
    made_at    timestamptz NOT NULL DEFAULT now()
);
