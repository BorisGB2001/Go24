-- «Генератор документов»: DOCX templates for CRM cards; a document is made from a template and a card.
CREATE TABLE crm_doc_templates (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    entity     text NOT NULL CHECK (entity IN ('deal', 'lead', 'contact', 'company')),
    file_id    uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document'));
