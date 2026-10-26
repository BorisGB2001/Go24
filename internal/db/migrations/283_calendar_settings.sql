-- Settings of one's calendar, as «Настройки календаря» (GO24DEV-428): the first day of the week, the
-- working hours (the hour grid of the day and the week opens on them, the rest is shaded), the days off, whether
-- declined meetings and task deadlines are shown, the reminder a new meeting gets. No row — the defaults.
CREATE TABLE calendar_settings (
    user_id       uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    week_start    smallint NOT NULL DEFAULT 1 CHECK (week_start BETWEEN 1 AND 7), -- 1 Monday … 7 Sunday
    work_from     smallint NOT NULL DEFAULT 9 CHECK (work_from BETWEEN 0 AND 23),
    work_to       smallint NOT NULL DEFAULT 18 CHECK (work_to BETWEEN 1 AND 24),
    days_off      smallint[] NOT NULL DEFAULT '{6,7}',
    show_declined boolean NOT NULL DEFAULT true,
    show_tasks    boolean NOT NULL DEFAULT true,
    remind        integer NOT NULL DEFAULT 15 -- minutes before the start; -1 — none
);
