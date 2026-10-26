-- Repetition (GO24DEV-682): a weekly meeting on chosen days of the week (1 — Monday … 7 — Sunday;
-- none — the day of its start) and a series that ends after so many meetings (0 — by repeat_until or never; with a
-- count repeat_until is the day of the last one).
ALTER TABLE events ADD COLUMN repeat_days integer[] NOT NULL DEFAULT '{}',
    ADD COLUMN repeat_count integer NOT NULL DEFAULT 0 CHECK (repeat_count BETWEEN 0 AND 999);
