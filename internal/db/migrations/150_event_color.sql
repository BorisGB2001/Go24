-- A meeting's colour and «Важная встреча», as in a portal meeting (GO24DEV-422): the colour paints it in the grid
-- ("" — the colour of its calendar), an important one is marked.
ALTER TABLE events ADD COLUMN color text NOT NULL DEFAULT '', ADD COLUMN important boolean NOT NULL DEFAULT false;
