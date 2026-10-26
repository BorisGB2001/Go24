-- «Онлайн-запись» («Подтверждение записи»): a resource may ask its clients to confirm the booking
-- confirm_hours before it (a letter with the booking's page), and tell its employee when one has not confirmed
-- confirm_alert_hours before it.
ALTER TABLE booking_resources
    ADD COLUMN confirm_hours       int NOT NULL DEFAULT 0 CHECK (confirm_hours BETWEEN 0 AND 336),
    ADD COLUMN confirm_alert_hours int NOT NULL DEFAULT 0 CHECK (confirm_alert_hours BETWEEN 0 AND 336);

ALTER TABLE bookings
    ADD COLUMN confirm_asked_at timestamptz, -- the letter asking to confirm went
    ADD COLUMN unconfirmed_at   timestamptz; -- the employee was told the client has not confirmed
