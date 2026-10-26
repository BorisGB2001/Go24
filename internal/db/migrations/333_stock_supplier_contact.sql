-- The supplier of «Приход» is a company or a contact of the CRM: one of the two.
ALTER TABLE crm_stock_docs ADD COLUMN supplier_contact_id uuid REFERENCES crm_contacts(id) ON DELETE SET NULL,
    ADD CONSTRAINT crm_stock_docs_one_supplier CHECK (supplier_id IS NULL OR supplier_contact_id IS NULL);
