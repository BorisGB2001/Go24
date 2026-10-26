-- Генератор документов: шаблоны и документы счетов и предложений CRM, их печатные формы (GO24DEV-668).
ALTER TABLE crm_doc_templates DROP CONSTRAINT crm_doc_templates_entity_check;
ALTER TABLE crm_doc_templates ADD CONSTRAINT crm_doc_templates_entity_check
    CHECK (entity IN ('deal', 'lead', 'contact', 'company', 'employee', 'invoice'));
ALTER TABLE crm_documents DROP CONSTRAINT crm_documents_entity_check;
ALTER TABLE crm_documents ADD CONSTRAINT crm_documents_entity_check
    CHECK (entity IN ('deal', 'lead', 'contact', 'company', 'invoice'));
