-- «Реквизиты» of the portal CRM: a company or a contact has requisites by a preset — an organization, a sole
-- trader (ИП) or a private person — with its bank details; one of them is the default, printed on the client's
-- quotes and invoices as the buyer.
CREATE TABLE crm_requisites (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity       text NOT NULL CHECK (entity IN ('company', 'contact')),
    entity_id    uuid NOT NULL,
    preset       text NOT NULL DEFAULT 'org' CHECK (preset IN ('org', 'ip', 'person')),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 300),
    inn          text NOT NULL DEFAULT '',
    kpp          text NOT NULL DEFAULT '',
    ogrn         text NOT NULL DEFAULT '',
    director     text NOT NULL DEFAULT '',
    address      text NOT NULL DEFAULT '',
    bank         text NOT NULL DEFAULT '',
    bik          text NOT NULL DEFAULT '',
    account      text NOT NULL DEFAULT '',
    corr_account text NOT NULL DEFAULT '',
    is_default   boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_requisites_idx ON crm_requisites (entity, entity_id);
CREATE INDEX crm_requisites_inn_idx ON crm_requisites (inn) WHERE inn <> '';
