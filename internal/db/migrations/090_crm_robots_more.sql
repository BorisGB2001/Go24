-- More «Роботы» of the portal CRM: plan a deed, write to the client, give the card to another responsible, move it
-- to another stage; a robot may wait before it runs (the queue below, run by the worker) and run only for an amount.
ALTER TABLE crm_robots DROP CONSTRAINT crm_robots_kind_check;
ALTER TABLE crm_robots ADD CONSTRAINT crm_robots_kind_check
    CHECK (kind IN ('task', 'notify', 'comment', 'activity', 'mail', 'responsible', 'stage'));

CREATE TABLE crm_robot_jobs (
    id        bigserial PRIMARY KEY,
    robot_id  uuid NOT NULL REFERENCES crm_robots(id) ON DELETE CASCADE,
    entity    text NOT NULL CHECK (entity IN ('deal', 'lead')),
    entity_id uuid NOT NULL,
    stage_id  uuid NOT NULL REFERENCES crm_stages(id) ON DELETE CASCADE,
    actor_id  uuid NOT NULL REFERENCES users(id),
    run_at    timestamptz NOT NULL
);
CREATE INDEX crm_robot_jobs_due_idx ON crm_robot_jobs (run_at);
