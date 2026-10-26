-- «Онлайн-запись»: letters to the client — about the booking (with the links «Подтвердить» and
-- «Отменить»), a reminder remind_hours before the visit, a note when the client is late_minutes late (the resource's
-- employee is told too); 0 — not sent. The letters go from the mailbox of the resource's employee, else of who booked.
ALTER TABLE booking_resources ADD COLUMN notify_mail boolean NOT NULL DEFAULT true,
    ADD COLUMN remind_hours int NOT NULL DEFAULT 24 CHECK (remind_hours BETWEEN 0 AND 336),
    ADD COLUMN late_minutes int NOT NULL DEFAULT 15 CHECK (late_minutes BETWEEN 0 AND 240);

-- what was done for a booking: the letter about it, the reminder, the lateness
ALTER TABLE bookings ADD COLUMN mailed_at timestamptz, ADD COLUMN reminded_at timestamptz, ADD COLUMN late_at timestamptz;
-- the bookings made before have no due letters
UPDATE bookings SET mailed_at = now(), reminded_at = now(), late_at = now();
UPDATE bookings SET client_token = md5(random()::text || id::text) WHERE client_token IS NULL;
