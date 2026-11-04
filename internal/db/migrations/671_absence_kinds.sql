-- «Типы отсутствий» (GO24DEV-843): the built-in reasons of the absence chart renamed, recoloured or
-- turned off, and the company's own ones (keys "c…"); an absence keeps any reason's key.
ALTER TABLE absences DROP CONSTRAINT IF EXISTS absences_kind_check;
CREATE TABLE absence_kinds (
    key        text PRIMARY KEY CHECK (key <> ''),
    name       text NOT NULL DEFAULT '' CHECK (length(name) <= 60), -- '' — the built-in name
    color      text NOT NULL DEFAULT '',
    off        boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
