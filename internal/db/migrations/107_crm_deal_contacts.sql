-- «Клиент» of a portal deal: a company and several contacts. The first one stays in crm_deals.contact_id (the
-- main contact: the robots write to him, his requisites go on the invoice when there is no company).
CREATE TABLE crm_deal_contacts (
    deal_id    uuid NOT NULL REFERENCES crm_deals(id) ON DELETE CASCADE,
    contact_id uuid NOT NULL REFERENCES crm_contacts(id) ON DELETE CASCADE,
    sort       int NOT NULL DEFAULT 0,
    PRIMARY KEY (deal_id, contact_id)
);
CREATE INDEX crm_deal_contacts_contact ON crm_deal_contacts (contact_id);

INSERT INTO crm_deal_contacts (deal_id, contact_id) SELECT id, contact_id FROM crm_deals WHERE contact_id IS NOT NULL;
