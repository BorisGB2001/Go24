-- «Генератор продаж»: repeat sales to the clients of a CRM segment (contacts or companies) — started, it
-- makes a «Повторный лид» or a deal of the chosen pipeline and stage for each client, by the client's responsible or the
-- chosen employees in turn; its progress is the cards made and how they ended.
CREATE TABLE mkt_salesgen (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    segment_id  uuid REFERENCES mkt_segments(id) ON DELETE SET NULL,
    target      text NOT NULL DEFAULT 'deal' CHECK (target IN ('lead', 'deal')),
    pipeline_id uuid REFERENCES crm_pipelines(id) ON DELETE SET NULL,
    stage_id    uuid REFERENCES crm_stages(id) ON DELETE SET NULL,
    card_title  text NOT NULL DEFAULT '',
    comment     text NOT NULL DEFAULT '',
    skip_open   boolean NOT NULL DEFAULT true,
    assign      text NOT NULL DEFAULT 'owner' CHECK (assign IN ('owner', 'staff')),
    staff       uuid[] NOT NULL DEFAULT '{}',
    status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'started', 'done')),
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- a client of a started generator; card_id — the lead or deal made for it (claimed_at set while being made)
CREATE TABLE mkt_salesgen_items (
    id             bigserial PRIMARY KEY,
    gen_id         uuid NOT NULL REFERENCES mkt_salesgen(id) ON DELETE CASCADE,
    entity         text NOT NULL CHECK (entity IN ('contact', 'company')),
    entity_id      uuid NOT NULL,
    name           text NOT NULL DEFAULT '',
    responsible_id uuid REFERENCES users(id) ON DELETE SET NULL,
    claimed_at     timestamptz,
    card_id        uuid,
    error          text NOT NULL DEFAULT ''
);
CREATE INDEX mkt_salesgen_items_gen ON mkt_salesgen_items (gen_id, id);
CREATE INDEX mkt_salesgen_items_todo ON mkt_salesgen_items (id) WHERE claimed_at IS NULL;
