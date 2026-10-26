-- «Онлайн-запись» («Обратная связь»): a resource may ask its clients to rate the visit — after it is over
-- and the client came, a letter with the booking's page, where the client gives 1–5 stars and a comment.
ALTER TABLE booking_resources ADD COLUMN feedback boolean NOT NULL DEFAULT false;

ALTER TABLE bookings
    ADD COLUMN feedback_at timestamptz,                          -- the letter asking to rate went
    ADD COLUMN rating      smallint CHECK (rating BETWEEN 1 AND 5),
    ADD COLUMN rating_note text NOT NULL DEFAULT '',
    ADD COLUMN rated_at    timestamptz;
