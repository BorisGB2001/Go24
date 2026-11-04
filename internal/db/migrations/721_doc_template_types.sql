-- Templates of the document generator for a smart process (GO24DEV-847): a template of deals with a
-- type is one of that smart process's cards only; without one — of deals.
ALTER TABLE crm_doc_templates ADD COLUMN type_id uuid REFERENCES crm_types(id) ON DELETE CASCADE,
    ADD CONSTRAINT crm_doc_templates_type_check CHECK (type_id IS NULL OR entity = 'deal');
