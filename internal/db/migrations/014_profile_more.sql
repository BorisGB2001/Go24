-- More employee card fields, as in the portal profile: gender, city, inner phone, employment date, time zone.
ALTER TABLE users
    ADD COLUMN gender          text NOT NULL DEFAULT '' CHECK (gender IN ('', 'M', 'F')),
    ADD COLUMN city            text NOT NULL DEFAULT '',
    ADD COLUMN inner_phone     text NOT NULL DEFAULT '', -- внутренний телефон (UF_PHONE_INNER)
    ADD COLUMN employment_date date,                     -- дата приёма на работу
    ADD COLUMN time_zone       text NOT NULL DEFAULT '';
