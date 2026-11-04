-- «Местоположение» of a working day, as the portal mobile time tracking (GO24DEV-968): with the setting on, the browser
-- tells where a day was started and finished (if the employee allows it); accuracy in metres. NULL — not known.
ALTER TABLE workdays
    ADD COLUMN start_lat double precision, ADD COLUMN start_lon double precision, ADD COLUMN start_acc int,
    ADD COLUMN end_lat double precision, ADD COLUMN end_lon double precision, ADD COLUMN end_acc int;
