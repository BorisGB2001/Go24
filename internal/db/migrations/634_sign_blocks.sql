-- «Подпись»: blocks on the pages of the document (GO24DEV-837): the sender puts a signer's signature,
-- name, signing date, the company's stamp or a text on a page; the signed PDF has them in their places.
CREATE TABLE sign_blocks (
    id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    doc_id   uuid NOT NULL REFERENCES sign_docs(id) ON DELETE CASCADE,
    party_id uuid NOT NULL REFERENCES sign_parties(id) ON DELETE CASCADE,
    kind     text NOT NULL CHECK (kind IN ('signature', 'name', 'date', 'stamp', 'text')),
    page     int NOT NULL CHECK (page >= 1),
    -- the box, in fractions of the page's size from its top left corner
    x        real NOT NULL CHECK (x >= 0 AND x < 1),
    y        real NOT NULL CHECK (y >= 0 AND y < 1),
    w        real NOT NULL CHECK (w > 0 AND w <= 1),
    h        real NOT NULL CHECK (h > 0 AND h <= 1),
    text     text NOT NULL DEFAULT '' CHECK (length(text) <= 200)
);
CREATE INDEX sign_blocks_doc ON sign_blocks (doc_id);
