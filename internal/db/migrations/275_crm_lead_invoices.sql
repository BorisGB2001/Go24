-- «Счёт и предложение из лида»: a quote or an invoice belongs to a deal or to a lead; the lead's go
-- to the deal it is converted into.
ALTER TABLE crm_invoices ALTER COLUMN deal_id DROP NOT NULL;
ALTER TABLE crm_invoices ADD COLUMN lead_id uuid REFERENCES crm_leads(id) ON DELETE CASCADE;
ALTER TABLE crm_invoices ADD CONSTRAINT crm_invoices_card_check CHECK ((deal_id IS NULL) <> (lead_id IS NULL));
CREATE INDEX crm_invoices_lead_idx ON crm_invoices (lead_id) WHERE lead_id IS NOT NULL;
