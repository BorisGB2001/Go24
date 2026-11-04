-- Options of a meeting for its people, as «Ещё» of the meeting's form: the invited may invite others,
-- the organizer does not hear of their answers (by default they do), the invited do not see one another.
ALTER TABLE events ADD COLUMN allow_invite boolean NOT NULL DEFAULT false,
    ADD COLUMN quiet_answers boolean NOT NULL DEFAULT false,
    ADD COLUMN hide_guests boolean NOT NULL DEFAULT false;
