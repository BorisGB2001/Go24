-- «Онлайн-запись»: the schedule of a resource by days — hours of their own for some weekdays (over the
-- common work_from–work_to), a break within the day, and dates off (holidays, a vacation) when it does not work.
ALTER TABLE booking_resources
    ADD COLUMN day_hours  jsonb  NOT NULL DEFAULT '{}', -- {"6": [600, 900]}: an ISO weekday → its from, to (minutes)
    ADD COLUMN break_from int    NOT NULL DEFAULT 0,    -- the break, minutes of the day; break_from = break_to — none
    ADD COLUMN break_to   int    NOT NULL DEFAULT 0,
    ADD COLUMN off_dates  date[] NOT NULL DEFAULT '{}';
