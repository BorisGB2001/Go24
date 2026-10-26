-- Employee photo uploaded in go24 (object key in the built-in storage). Without one the card shows the
-- go24 picture; photos are never imported.
ALTER TABLE users ADD COLUMN photo_key text NOT NULL DEFAULT '', ADD COLUMN photo_type text NOT NULL DEFAULT '';
