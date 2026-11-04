-- «Подпись»: fields the signer fills in on the pages of the document (GO24DEV-850): the sender puts
-- a «field» block with its caption (text) — «ИНН», «Адрес» — required or not; the signer gives its value on signing.
ALTER TABLE sign_blocks DROP CONSTRAINT sign_blocks_kind_check;
ALTER TABLE sign_blocks ADD CONSTRAINT sign_blocks_kind_check CHECK (kind IN ('signature', 'name', 'date', 'stamp', 'text', 'field'));
ALTER TABLE sign_blocks ADD COLUMN required bool NOT NULL DEFAULT false;
ALTER TABLE sign_blocks ADD COLUMN value text NOT NULL DEFAULT '' CHECK (length(value) <= 200);
