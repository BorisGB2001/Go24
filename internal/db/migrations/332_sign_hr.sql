-- «Подпись для сотрудников» (КЭДО) (GO24DEV-620): кадровый документ рассылается многим сотрудникам —
-- у каждого свой экземпляр с его полями (шаблон «Сотрудник» генератора документов); сначала, если задан, подписывает
-- представитель компании, потом сотрудник. Экземпляры одной рассылки собраны в sign_batches.
ALTER TABLE crm_doc_templates DROP CONSTRAINT crm_doc_templates_entity_check;
ALTER TABLE crm_doc_templates ADD CONSTRAINT crm_doc_templates_entity_check
    CHECK (entity IN ('deal', 'lead', 'contact', 'company', 'employee'));

CREATE TABLE sign_batches (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    template_id uuid REFERENCES crm_doc_templates(id) ON DELETE SET NULL,
    signer_id   uuid REFERENCES users(id),                                    -- the company's representative, if any
    created_by  uuid NOT NULL REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sign_batches_author ON sign_batches (created_by, created_at DESC);

ALTER TABLE sign_docs ADD COLUMN batch_id uuid REFERENCES sign_batches(id) ON DELETE CASCADE;
CREATE INDEX sign_docs_batch ON sign_docs (batch_id) WHERE batch_id IS NOT NULL;

-- The signer's side on the signing sheet: '' — by who they are (an employee signs for the company, the rest are the
-- client), 'employee' — the employee the HR document is for.
ALTER TABLE sign_parties ADD COLUMN side text NOT NULL DEFAULT '' CHECK (side IN ('', 'employee'));
