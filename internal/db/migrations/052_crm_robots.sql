-- «Роботы» of the portal CRM: when a deal or a lead enters a stage, go24 does what the robots of that stage say —
-- makes a task, notifies someone, writes into the card's history.
CREATE TABLE crm_robots (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stage_id   uuid NOT NULL REFERENCES crm_stages(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('task', 'notify', 'comment')),
    params     jsonb NOT NULL DEFAULT '{}',
    active     boolean NOT NULL DEFAULT true,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_robots_stage_idx ON crm_robots (stage_id) WHERE active;

ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot'));

-- a notification may lead anywhere in go24 (a robot's message about a CRM card)
ALTER TABLE notifications ADD COLUMN url text;
