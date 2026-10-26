-- «Обязательное поле» of the CRM cards: a field required always or only at the chosen stages of
-- deals and leads (required_stages — crm_stages ids; empty — at every stage). The card form, the kanban and the
-- stage bar ask to fill it before saving or moving the card. The columns are on every set of own fields (the store
-- reads them alike), but only the CRM settings offer them.
ALTER TABLE task_fields ADD COLUMN required bool NOT NULL DEFAULT false, ADD COLUMN required_stages text[] NOT NULL DEFAULT '{}';
ALTER TABLE user_fields ADD COLUMN required bool NOT NULL DEFAULT false, ADD COLUMN required_stages text[] NOT NULL DEFAULT '{}';
ALTER TABLE crm_deal_fields ADD COLUMN required bool NOT NULL DEFAULT false, ADD COLUMN required_stages text[] NOT NULL DEFAULT '{}';
ALTER TABLE crm_lead_fields ADD COLUMN required bool NOT NULL DEFAULT false, ADD COLUMN required_stages text[] NOT NULL DEFAULT '{}';
ALTER TABLE crm_contact_fields ADD COLUMN required bool NOT NULL DEFAULT false, ADD COLUMN required_stages text[] NOT NULL DEFAULT '{}';
ALTER TABLE crm_company_fields ADD COLUMN required bool NOT NULL DEFAULT false, ADD COLUMN required_stages text[] NOT NULL DEFAULT '{}';
