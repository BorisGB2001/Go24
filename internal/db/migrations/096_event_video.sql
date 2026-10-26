-- A video meeting of go24, as «Видеоконференция» in a portal meeting (GO24DEV-237): the meeting has its call
-- (LiveKit) its people join from the meeting's card and the reminder.
ALTER TABLE events ADD COLUMN video boolean NOT NULL DEFAULT false;
