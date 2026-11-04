-- «Подпись»: the signing deadline (GO24DEV-819): a document not signed by the end of its deadline day
-- is closed as expired; a template of sending keeps the deadline as days from the sending.
ALTER TABLE sign_docs ADD COLUMN deadline date;
ALTER TABLE sign_docs DROP CONSTRAINT sign_docs_status_check;
ALTER TABLE sign_docs ADD CONSTRAINT sign_docs_status_check CHECK (status IN ('signing', 'done', 'refused', 'cancelled', 'expired'));
CREATE INDEX sign_docs_deadline ON sign_docs (deadline) WHERE status = 'signing' AND deadline IS NOT NULL;
ALTER TABLE sign_templates ADD COLUMN deadline_days int NOT NULL DEFAULT 0 CHECK (deadline_days BETWEEN 0 AND 365);
