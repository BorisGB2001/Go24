-- «Видимость поля» of the CRM's own fields (GO24DEV-992): who sees a field — employees (user ids) and
-- departments («dept:<id>», with the ones below); empty — everyone. Administrators see every field. The column is on
-- every set of own fields (they are read alike), but only the CRM offers it.
ALTER TABLE task_fields ADD COLUMN visible_to text[] NOT NULL DEFAULT '{}';
ALTER TABLE user_fields ADD COLUMN visible_to text[] NOT NULL DEFAULT '{}';
ALTER TABLE crm_deal_fields ADD COLUMN visible_to text[] NOT NULL DEFAULT '{}';
ALTER TABLE crm_lead_fields ADD COLUMN visible_to text[] NOT NULL DEFAULT '{}';
ALTER TABLE crm_contact_fields ADD COLUMN visible_to text[] NOT NULL DEFAULT '{}';
ALTER TABLE crm_company_fields ADD COLUMN visible_to text[] NOT NULL DEFAULT '{}';
