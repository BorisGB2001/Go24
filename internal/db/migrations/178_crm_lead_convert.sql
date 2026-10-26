-- «Конвертация» of a lead: besides a deal, it may give a contact and a company (new or existing
-- ones) — the lead keeps all three.
ALTER TABLE crm_leads
    ADD COLUMN contact_id uuid REFERENCES crm_contacts(id) ON DELETE SET NULL,
    ADD COLUMN company_id uuid REFERENCES crm_companies(id) ON DELETE SET NULL;
