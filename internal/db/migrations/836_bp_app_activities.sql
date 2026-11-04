-- Actions of applications for the workflow designer, as bizproc.activity.add (GO24DEV-903): a step
-- posts its properties to the application's handler; with subscribe the process waits for bizproc.event.send by the
-- step's token (wait_app) and puts the returned values into its fields.
CREATE TABLE app_bp_activities (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id      uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    code        text NOT NULL CHECK (code ~ '^[A-Za-z0-9_.-]{1,100}$'),
    handler_url text NOT NULL CHECK (handler_url ~ '^https?://'),
    user_id     uuid REFERENCES users(id) ON DELETE SET NULL, -- whose tokens the handler gets (AUTH_USER_ID); none — the starter's
    subscribe   boolean NOT NULL DEFAULT false,               -- USE_SUBSCRIPTION: the process waits for the answer
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    props       jsonb NOT NULL DEFAULT '[]',                  -- PROPERTIES
    returns     jsonb NOT NULL DEFAULT '[]',                  -- RETURN_PROPERTIES
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (app_id, code)
);

ALTER TABLE bp_instances ADD COLUMN wait_app text;
ALTER TABLE bp_branches ADD COLUMN wait_app text;
CREATE UNIQUE INDEX bp_instances_wait_app_idx ON bp_instances (wait_app) WHERE wait_app IS NOT NULL;
CREATE UNIQUE INDEX bp_branches_wait_app_idx ON bp_branches (wait_app) WHERE wait_app IS NOT NULL;
