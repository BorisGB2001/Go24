-- «Бизнес-процессы», a linear form: a template is a form and a list of steps (approval, task,
-- notification, CRM stage, pause); a process runs the steps one by one, waiting on approvals and pauses.
CREATE TABLE bp_templates (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '',
    entity      text NOT NULL DEFAULT 'none' CHECK (entity IN ('none', 'deal', 'lead', 'contact', 'company')),
    fields      jsonb NOT NULL DEFAULT '[]', -- [{"key", "label", "kind": string|text|number|date, "required"}]
    steps       jsonb NOT NULL DEFAULT '[]', -- [{"kind": approve|task|notify|stage|wait, …params}]
    active      boolean NOT NULL DEFAULT true,
    created_by  uuid NOT NULL REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE bp_instances (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    template_id uuid NOT NULL REFERENCES bp_templates(id) ON DELETE CASCADE,
    name        text NOT NULL,                 -- the template's name when started
    steps       jsonb NOT NULL,                -- the steps as they were when started (editing the template does not change it)
    entity      text NOT NULL DEFAULT 'none',
    entity_id   uuid,
    values      jsonb NOT NULL DEFAULT '{}',   -- the form
    status      text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'rejected', 'failed')),
    step        int NOT NULL DEFAULT 0,        -- the step running or waited on
    wake_at     timestamptz,                   -- a pause ends
    started_by  uuid NOT NULL REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX bp_instances_wake_idx ON bp_instances (wake_at) WHERE status = 'running' AND wake_at IS NOT NULL;
CREATE INDEX bp_instances_entity_idx ON bp_instances (entity, entity_id);
CREATE TABLE bp_approvals (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id uuid NOT NULL REFERENCES bp_instances(id) ON DELETE CASCADE,
    step        int NOT NULL,
    user_id     uuid NOT NULL REFERENCES users(id),
    text        text NOT NULL DEFAULT '',
    decision    text CHECK (decision IN ('approve', 'reject')),
    comment     text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    decided_at  timestamptz
);
CREATE INDEX bp_approvals_user_idx ON bp_approvals (user_id) WHERE decision IS NULL;
CREATE TABLE bp_log (
    id          bigserial PRIMARY KEY,
    instance_id uuid NOT NULL REFERENCES bp_instances(id) ON DELETE CASCADE,
    step        int NOT NULL,
    actor_id    uuid REFERENCES users(id),
    kind        text NOT NULL,  -- started | step | approved | rejected | done | failed
    text        text NOT NULL DEFAULT '',
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bp_log_idx ON bp_log (instance_id, id);
