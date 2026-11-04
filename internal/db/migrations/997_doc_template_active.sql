-- «Активен», «Сортировка» and «С печатями и подписями» of a template of the document generator, as ACTIVE, SORT and
-- WITH_STAMPS (GO24DEV-994): an inactive template is not offered for new documents; who changed it last.
ALTER TABLE crm_doc_templates ADD COLUMN active boolean NOT NULL DEFAULT true,
    ADD COLUMN sort integer NOT NULL DEFAULT 500 CHECK (sort BETWEEN 0 AND 1000000),
    ADD COLUMN with_stamps boolean NOT NULL DEFAULT true,
    ADD COLUMN updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN updated_at timestamptz;
