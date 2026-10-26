-- «Пользовательские поля» of the CRM cards, like those of tasks (038) and profiles (046): own fields of deals, leads,
-- contacts and companies, set up by the administrator.
CREATE TABLE crm_deal_fields (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind       text NOT NULL CHECK (kind IN ('string', 'number', 'date', 'bool', 'list', 'url')),
    options    text[] NOT NULL DEFAULT '{}',
    sort       int NOT NULL DEFAULT 100,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX crm_deal_fields_name_idx ON crm_deal_fields (lower(name));
CREATE TABLE crm_deal_field_values (
    deal_id  uuid NOT NULL REFERENCES crm_deals(id) ON DELETE CASCADE,
    field_id uuid NOT NULL REFERENCES crm_deal_fields(id) ON DELETE CASCADE,
    value    text NOT NULL CHECK (length(value) BETWEEN 1 AND 2000),
    PRIMARY KEY (deal_id, field_id)
);
CREATE INDEX crm_deal_field_values_field_idx ON crm_deal_field_values (field_id);

CREATE TABLE crm_lead_fields (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind       text NOT NULL CHECK (kind IN ('string', 'number', 'date', 'bool', 'list', 'url')),
    options    text[] NOT NULL DEFAULT '{}',
    sort       int NOT NULL DEFAULT 100,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX crm_lead_fields_name_idx ON crm_lead_fields (lower(name));
CREATE TABLE crm_lead_field_values (
    lead_id  uuid NOT NULL REFERENCES crm_leads(id) ON DELETE CASCADE,
    field_id uuid NOT NULL REFERENCES crm_lead_fields(id) ON DELETE CASCADE,
    value    text NOT NULL CHECK (length(value) BETWEEN 1 AND 2000),
    PRIMARY KEY (lead_id, field_id)
);
CREATE INDEX crm_lead_field_values_field_idx ON crm_lead_field_values (field_id);

CREATE TABLE crm_contact_fields (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind       text NOT NULL CHECK (kind IN ('string', 'number', 'date', 'bool', 'list', 'url')),
    options    text[] NOT NULL DEFAULT '{}',
    sort       int NOT NULL DEFAULT 100,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX crm_contact_fields_name_idx ON crm_contact_fields (lower(name));
CREATE TABLE crm_contact_field_values (
    contact_id  uuid NOT NULL REFERENCES crm_contacts(id) ON DELETE CASCADE,
    field_id uuid NOT NULL REFERENCES crm_contact_fields(id) ON DELETE CASCADE,
    value    text NOT NULL CHECK (length(value) BETWEEN 1 AND 2000),
    PRIMARY KEY (contact_id, field_id)
);
CREATE INDEX crm_contact_field_values_field_idx ON crm_contact_field_values (field_id);

CREATE TABLE crm_company_fields (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind       text NOT NULL CHECK (kind IN ('string', 'number', 'date', 'bool', 'list', 'url')),
    options    text[] NOT NULL DEFAULT '{}',
    sort       int NOT NULL DEFAULT 100,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX crm_company_fields_name_idx ON crm_company_fields (lower(name));
CREATE TABLE crm_company_field_values (
    company_id  uuid NOT NULL REFERENCES crm_companies(id) ON DELETE CASCADE,
    field_id uuid NOT NULL REFERENCES crm_company_fields(id) ON DELETE CASCADE,
    value    text NOT NULL CHECK (length(value) BETWEEN 1 AND 2000),
    PRIMARY KEY (company_id, field_id)
);
CREATE INDEX crm_company_field_values_field_idx ON crm_company_field_values (field_id);

