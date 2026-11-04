-- GO24DEV-904: «С печатями и подписями» of a document of a card, as stampsEnabled of the document generator of
-- the portal: off — the stamp and the facsimile signatures of «Моя компания» are left out of the document.
ALTER TABLE crm_documents ADD COLUMN IF NOT EXISTS stamps boolean NOT NULL DEFAULT true;
