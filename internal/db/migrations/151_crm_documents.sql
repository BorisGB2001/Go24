-- «Генератор документов»: сделанные документы хранятся у карточки, у шаблона — нумератор.
ALTER TABLE crm_doc_templates ADD COLUMN numerator text NOT NULL DEFAULT '{NUMBER}' CHECK (length(numerator) BETWEEN 1 AND 100),
    ADD COLUMN counter bigint NOT NULL DEFAULT 0;
CREATE TABLE crm_documents (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    template_id   uuid REFERENCES crm_doc_templates(id) ON DELETE SET NULL,
    template_name text NOT NULL,
    entity        text NOT NULL CHECK (entity IN ('deal', 'lead', 'contact', 'company')),
    entity_id     uuid NOT NULL,
    number        text NOT NULL DEFAULT '',
    file_id       uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    values        jsonb NOT NULL DEFAULT '{}',
    created_by    uuid NOT NULL REFERENCES users(id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_documents_card ON crm_documents (entity, entity_id, created_at DESC);
