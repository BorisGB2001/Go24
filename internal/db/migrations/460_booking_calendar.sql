-- «Онлайн-запись»: a resource that is an employee goes by their calendar — meetings and absences take its slots.
ALTER TABLE booking_resources ADD COLUMN calendar_busy boolean NOT NULL DEFAULT true;
