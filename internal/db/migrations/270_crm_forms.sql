-- «CRM-формы» (GO24DEV-382): a form a client fills on its public page or on a site where it is embedded;
-- each filling makes a lead (or a contact with a deal) and is kept among the form's results.
CREATE TABLE crm_forms (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token          text NOT NULL UNIQUE,
    name           text NOT NULL,
    active         boolean NOT NULL DEFAULT true,
    scheme         text NOT NULL DEFAULT 'lead' CHECK (scheme IN ('lead', 'deal')),
    pipeline_id    uuid REFERENCES crm_pipelines(id) ON DELETE SET NULL,
    responsible_id uuid REFERENCES users(id) ON DELETE SET NULL,
    title          text NOT NULL DEFAULT '',
    text           text NOT NULL DEFAULT '',
    button         text NOT NULL DEFAULT '',
    success        text NOT NULL DEFAULT '',
    redirect       text NOT NULL DEFAULT '',
    consent        text NOT NULL DEFAULT '',
    fields         jsonb NOT NULL DEFAULT '[]',
    created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE crm_form_results (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    form_id    uuid NOT NULL REFERENCES crm_forms(id) ON DELETE CASCADE,
    answers    jsonb NOT NULL,
    lead_id    uuid REFERENCES crm_leads(id) ON DELETE SET NULL,
    contact_id uuid REFERENCES crm_contacts(id) ON DELETE SET NULL,
    deal_id    uuid REFERENCES crm_deals(id) ON DELETE SET NULL,
    ip         text NOT NULL DEFAULT '',
    page       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_form_results_form_idx ON crm_form_results (form_id, created_at DESC);
