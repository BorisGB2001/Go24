-- Working hours of an open line: the line's own time zone ("" — go24's) and its holidays
-- ("MM-DD" every year or "YYYY-MM-DD"), when the line does not work.
ALTER TABLE ol_widgets ADD COLUMN IF NOT EXISTS hours_tz text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS holidays text[] NOT NULL DEFAULT '{}';
