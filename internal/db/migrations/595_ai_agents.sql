-- The agents of the sections (GO24DEV-481): a run is a conversation of an employee with an agent, the agent works for
-- them with their rights. ai_actions is the log of the tools it called; a tool that writes or sends out waits
-- (pending) for the employee to confirm or reject it. ai_usage is every call of a model with its tokens (GO24DEV-482).
CREATE TABLE ai_runs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent         text NOT NULL,
    title         text NOT NULL DEFAULT '',
    connection_id uuid REFERENCES ai_connections(id) ON DELETE SET NULL,
    model         text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'done', -- running | confirm | done | error
    error         text NOT NULL DEFAULT '',
    messages      jsonb NOT NULL DEFAULT '[]',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_runs_user ON ai_runs (user_id, updated_at DESC);
CREATE TABLE ai_actions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id     uuid NOT NULL REFERENCES ai_runs(id) ON DELETE CASCADE,
    call_id    text NOT NULL,
    tool       text NOT NULL,
    args       jsonb NOT NULL DEFAULT '{}',
    write      boolean NOT NULL DEFAULT false,
    status     text NOT NULL, -- done | error | pending | rejected
    result     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz
);
CREATE INDEX ai_actions_run ON ai_actions (run_id, created_at);
CREATE TABLE ai_usage (
    id            bigserial PRIMARY KEY,
    user_id       uuid REFERENCES users(id) ON DELETE SET NULL,
    connection_id uuid REFERENCES ai_connections(id) ON DELETE SET NULL,
    company       boolean NOT NULL DEFAULT false, -- through a connection of the company
    run_id        uuid REFERENCES ai_runs(id) ON DELETE SET NULL,
    agent         text NOT NULL DEFAULT '',
    provider      text NOT NULL,
    model         text NOT NULL,
    in_tokens     integer NOT NULL DEFAULT 0,
    out_tokens    integer NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_usage_user ON ai_usage (user_id, created_at);
