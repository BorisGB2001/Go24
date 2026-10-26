-- Repeated meetings (GO24DEV-234): an occurrence changed or deleted on its own. A deleted one is an
-- exception of the series (by its original start); a changed one becomes a meeting of its own that remembers the
-- series and the occurrence it replaces, and goes away with the series.
CREATE TABLE event_exceptions (
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    at       timestamptz NOT NULL, -- the original start of the occurrence
    PRIMARY KEY (event_id, at)
);
ALTER TABLE events
    ADD COLUMN series_id uuid REFERENCES events(id) ON DELETE CASCADE,
    ADD COLUMN series_at timestamptz;
