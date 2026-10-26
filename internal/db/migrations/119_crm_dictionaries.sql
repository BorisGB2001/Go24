-- «Справочники» of the portal CRM: the lists the cards choose from — the source of a lead, a deal and a contact, the
-- type of a contact, of a company and of a deal, a company's industry. Built-in items have a code (their name is in the
-- locales while name is empty); administrators rename, add, order and delete them. A card keeps the item's id.
CREATE TABLE crm_dict (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN ('source', 'contact_type', 'company_type', 'industry', 'deal_type')),
    code text,
    name text NOT NULL DEFAULT '',
    sort int NOT NULL DEFAULT 100,
    UNIQUE (kind, code)
);

INSERT INTO crm_dict (kind, code, sort)
    SELECT 'source', c, n * 10 FROM unnest(ARRAY['CALL', 'EMAIL', 'WEB', 'ADVERTISING', 'EXISTING', 'RECOMMENDATION', 'TRADE_SHOW',
        'WEBFORM', 'CALLBACK', 'OPENLINE', 'STORE', 'OTHER']) WITH ORDINALITY AS u(c, n)
    UNION ALL SELECT 'contact_type', c, n * 10 FROM unnest(ARRAY['CLIENT', 'SUPPLIER', 'PARTNER', 'OTHER']) WITH ORDINALITY AS u(c, n)
    UNION ALL SELECT 'company_type', c, n * 10 FROM unnest(ARRAY['CLIENT', 'SUPPLIER', 'COMPETITOR', 'PARTNER', 'OTHER']) WITH ORDINALITY AS u(c, n)
    UNION ALL SELECT 'industry', c, n * 10 FROM unnest(ARRAY['IT', 'TELECOM', 'MANUFACTURING', 'BANKING', 'CONSULTING', 'FINANCE',
        'GOVERNMENT', 'DELIVERY', 'ENTERTAINMENT', 'NOTPROFIT', 'OTHER']) WITH ORDINALITY AS u(c, n)
    UNION ALL SELECT 'deal_type', c, n * 10 FROM unnest(ARRAY['SALE', 'COMPLEX', 'GOODS', 'SERVICES', 'SERVICE']) WITH ORDINALITY AS u(c, n);

ALTER TABLE crm_leads ADD COLUMN source_id uuid REFERENCES crm_dict (id) ON DELETE SET NULL;
ALTER TABLE crm_deals ADD COLUMN source_id uuid REFERENCES crm_dict (id) ON DELETE SET NULL,
    ADD COLUMN deal_type_id uuid REFERENCES crm_dict (id) ON DELETE SET NULL;
ALTER TABLE crm_contacts ADD COLUMN source_id uuid REFERENCES crm_dict (id) ON DELETE SET NULL,
    ADD COLUMN contact_type_id uuid REFERENCES crm_dict (id) ON DELETE SET NULL;
ALTER TABLE crm_companies ADD COLUMN company_type_id uuid REFERENCES crm_dict (id) ON DELETE SET NULL,
    ADD COLUMN industry_id uuid REFERENCES crm_dict (id) ON DELETE SET NULL;

-- the leads made so far: those of the mail and of the open lines get their source; the old text stays as
-- «Дополнительно об источнике»
UPDATE crm_leads l SET source_id = (SELECT id FROM crm_dict WHERE kind = 'source' AND code = 'EMAIL') WHERE l.source IN ('Почта', 'Mail');
UPDATE crm_leads l SET source_id = (SELECT id FROM crm_dict WHERE kind = 'source' AND code = 'OPENLINE')
    WHERE l.source_id IS NULL AND EXISTS (SELECT 1 FROM ol_sessions s WHERE s.lead_id = l.id);
