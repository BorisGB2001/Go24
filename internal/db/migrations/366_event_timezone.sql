-- The meeting's time zone (GO24DEV-618): its time is entered and its repetitions go by the clock of
-- that zone (an IANA name, '' — the portal's, GO24_TZ).
ALTER TABLE events ADD COLUMN timezone text NOT NULL DEFAULT '';
