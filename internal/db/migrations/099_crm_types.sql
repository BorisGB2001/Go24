-- «Смарт-процессы» of the portal CRM: the administrator's own kinds of cards. A kind has its own pipelines and
-- stages; its cards are kept as deals in those pipelines, so the kanban, the card, deeds, goods, robots and triggers
-- work for them as for deals. Pipelines without a kind are the deal pipelines.
CREATE TABLE crm_types (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    color      text NOT NULL DEFAULT '39A8EF',
    sort       int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE crm_pipelines ADD COLUMN type_id uuid REFERENCES crm_types(id) ON DELETE CASCADE;
CREATE INDEX crm_pipelines_type_idx ON crm_pipelines (type_id);
