-- Business processes, autostart (GO24DEV-385): a template of a CRM card starts by itself when a card
-- is added or changed; a template of deals may be one of a smart process (its type), as the card's deals are.
ALTER TABLE bp_templates ADD COLUMN auto_create boolean NOT NULL DEFAULT false;
ALTER TABLE bp_templates ADD COLUMN auto_change boolean NOT NULL DEFAULT false;
ALTER TABLE bp_templates ADD COLUMN type_id uuid REFERENCES crm_types(id) ON DELETE CASCADE;
-- the worker reads the CRM timeline after this event; the history before autostart existed starts nothing
CREATE TABLE bp_cursor (
    id      boolean PRIMARY KEY DEFAULT true CHECK (id),
    last_id bigint NOT NULL
);
INSERT INTO bp_cursor (last_id) SELECT coalesce(max(id), 0) FROM crm_timeline;
