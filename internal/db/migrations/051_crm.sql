-- CRM: companies, contacts, leads and deals; deals move along the stages of a pipeline («воронка»),
-- leads along their own stages. Every card keeps a timeline (comments and changes).
CREATE TABLE crm_companies (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title          text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    phone          text NOT NULL DEFAULT '',
    email          text NOT NULL DEFAULT '',
    website        text NOT NULL DEFAULT '',
    address        text NOT NULL DEFAULT '',
    comment        text NOT NULL DEFAULT '',
    responsible_id uuid NOT NULL REFERENCES users(id),
    created_by     uuid NOT NULL REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE crm_contacts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 300),
    position       text NOT NULL DEFAULT '',
    phone          text NOT NULL DEFAULT '',
    email          text NOT NULL DEFAULT '',
    company_id     uuid REFERENCES crm_companies(id) ON DELETE SET NULL,
    comment        text NOT NULL DEFAULT '',
    responsible_id uuid NOT NULL REFERENCES users(id),
    created_by     uuid NOT NULL REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_contacts_company_idx ON crm_contacts (company_id);

CREATE TABLE crm_pipelines (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text,                -- a built-in pipeline: its name comes from the locales (crm.pipeline.<code>)
    name       text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    sort       int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (code IS NOT NULL OR name <> '')
);
-- stages of a deal pipeline (pipeline_id) or of leads (pipeline_id NULL, entity 'lead')
CREATE TABLE crm_stages (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity      text NOT NULL CHECK (entity IN ('deal', 'lead')),
    pipeline_id uuid REFERENCES crm_pipelines(id) ON DELETE CASCADE,
    code        text,                -- a built-in stage: its name comes from the locales (crm.stage.<code>)
    name        text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    color       text NOT NULL DEFAULT '00C4FB',
    semantic    text NOT NULL DEFAULT 'process' CHECK (semantic IN ('process', 'success', 'failure')),
    sort        int NOT NULL DEFAULT 0,
    CHECK ((entity = 'deal') = (pipeline_id IS NOT NULL)),
    CHECK (code IS NOT NULL OR name <> '')
);
CREATE INDEX crm_stages_idx ON crm_stages (entity, pipeline_id, sort);

CREATE TABLE crm_deals (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title          text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    pipeline_id    uuid NOT NULL REFERENCES crm_pipelines(id),
    stage_id       uuid NOT NULL REFERENCES crm_stages(id),
    amount         numeric(18, 2) NOT NULL DEFAULT 0,
    currency       text NOT NULL DEFAULT 'RUB',
    company_id     uuid REFERENCES crm_companies(id) ON DELETE SET NULL,
    contact_id     uuid REFERENCES crm_contacts(id) ON DELETE SET NULL,
    responsible_id uuid NOT NULL REFERENCES users(id),
    close_date     date,
    comment        text NOT NULL DEFAULT '',
    closed_at      timestamptz,
    created_by     uuid NOT NULL REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_deals_stage_idx ON crm_deals (pipeline_id, stage_id);

CREATE TABLE crm_leads (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title          text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    name           text NOT NULL DEFAULT '',
    company_title  text NOT NULL DEFAULT '',
    phone          text NOT NULL DEFAULT '',
    email          text NOT NULL DEFAULT '',
    source         text NOT NULL DEFAULT '',
    amount         numeric(18, 2) NOT NULL DEFAULT 0,
    currency       text NOT NULL DEFAULT 'RUB',
    stage_id       uuid NOT NULL REFERENCES crm_stages(id),
    responsible_id uuid NOT NULL REFERENCES users(id),
    comment        text NOT NULL DEFAULT '',
    deal_id        uuid REFERENCES crm_deals(id) ON DELETE SET NULL, -- what the lead was converted into
    created_by     uuid NOT NULL REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_leads_stage_idx ON crm_leads (stage_id);

CREATE TABLE crm_timeline (
    id        bigserial PRIMARY KEY,
    entity    text NOT NULL CHECK (entity IN ('company', 'contact', 'deal', 'lead')),
    entity_id uuid NOT NULL,
    author_id uuid NOT NULL REFERENCES users(id),
    kind      text NOT NULL CHECK (kind IN ('comment', 'created', 'stage', 'converted')),
    body      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_timeline_idx ON crm_timeline (entity, entity_id, id DESC);

-- the default pipeline and stages, as a new the portal has them (names in the locales)
WITH p AS (INSERT INTO crm_pipelines (code, sort) VALUES ('default', 0) RETURNING id)
INSERT INTO crm_stages (entity, pipeline_id, code, color, semantic, sort)
SELECT 'deal', p.id, s.code, s.color, s.semantic, s.sort FROM p, (VALUES
    ('new', '39A8EF', 'process', 10), ('preparation', '2FC6F6', 'process', 20),
    ('prepayment', '55D0E0', 'process', 30), ('executing', '47E4C2', 'process', 40),
    ('final_invoice', 'FFA900', 'process', 50), ('won', '7BD500', 'success', 90),
    ('lose', 'FF5752', 'failure', 100)) AS s(code, color, semantic, sort);
INSERT INTO crm_stages (entity, code, color, semantic, sort) VALUES
    ('lead', 'lead_new', '39A8EF', 'process', 10), ('lead', 'lead_in_process', '2FC6F6', 'process', 20),
    ('lead', 'lead_processed', 'FFA900', 'process', 30), ('lead', 'lead_converted', '7BD500', 'success', 90),
    ('lead', 'lead_junk', 'FF5752', 'failure', 100);
