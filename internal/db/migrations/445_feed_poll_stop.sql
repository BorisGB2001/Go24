-- «Опрос» of a feed post: an anonymous poll shows the counts only, without the names; the author (or an
-- admin) stops the poll — then nobody votes and everyone sees the results — and resumes it.
ALTER TABLE feed_polls
    ADD COLUMN anonymous boolean NOT NULL DEFAULT false,
    ADD COLUMN closed_at timestamptz;
