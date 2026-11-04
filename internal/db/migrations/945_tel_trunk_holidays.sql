-- Working hours of an external telephony line: the line's own time zone ("" — go24's) and its holidays
-- ("MM-DD" every year or "YYYY-MM-DD"), when its calls go as out of the working hours.
ALTER TABLE telephony_trunks ADD COLUMN IF NOT EXISTS work_tz text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS holidays text[] NOT NULL DEFAULT '{}';
