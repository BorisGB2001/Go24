-- Several phones and e-mails of a contact, company or lead «Телефон» and «E-mail» with a type
-- (work, mobile, home, other). The first of a kind is the card's main one, kept in its phone or email column too.
-- key is how they are matched (duplicates, repeat leads, the client of a call): a phone's last 10 digits ("" — fewer
-- than 7), an e-mail in lower case.
CREATE TABLE crm_comms (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity    text NOT NULL CHECK (entity IN ('contact', 'company', 'lead')),
    entity_id uuid NOT NULL,
    kind      text NOT NULL CHECK (kind IN ('phone', 'email')),
    type      text NOT NULL DEFAULT 'work',
    value     text NOT NULL,
    key       text NOT NULL DEFAULT '',
    sort      int NOT NULL DEFAULT 0
);
CREATE INDEX crm_comms_card ON crm_comms (entity, entity_id);
CREATE INDEX crm_comms_key ON crm_comms (kind, key) WHERE key <> '';

INSERT INTO crm_comms (entity, entity_id, kind, value, key)
SELECT e.entity, e.id, 'phone', e.phone, CASE WHEN length(regexp_replace(e.phone, '\D', '', 'g')) >= 7
        THEN right(regexp_replace(e.phone, '\D', '', 'g'), 10) ELSE '' END
    FROM (SELECT 'contact' AS entity, id, phone FROM crm_contacts UNION ALL SELECT 'company', id, phone FROM crm_companies
        UNION ALL SELECT 'lead', id, phone FROM crm_leads) e
    WHERE btrim(e.phone) <> '';
INSERT INTO crm_comms (entity, entity_id, kind, value, key)
SELECT e.entity, e.id, 'email', e.email, lower(btrim(e.email))
    FROM (SELECT 'contact' AS entity, id, email FROM crm_contacts UNION ALL SELECT 'company', id, email FROM crm_companies
        UNION ALL SELECT 'lead', id, email FROM crm_leads) e
    WHERE btrim(e.email) <> '';
