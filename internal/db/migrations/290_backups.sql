-- «Настройки → Резервное копирование» (GO24DEV-569): the copies made and asked for. The settings are a company
-- setting (company_settings "backup"); go24-worker takes a queued copy, makes it and writes how it went.
CREATE TABLE backup_runs (
    id          bigserial PRIMARY KEY,
    status      text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL, -- NULL — by the schedule
    started_at  timestamptz,
    finished_at timestamptz,
    name        text NOT NULL DEFAULT '',   -- the folder of the copy
    parts       text[] NOT NULL DEFAULT '{}', -- what it holds: go24, authentik, synapse, config, secrets
    size        bigint NOT NULL DEFAULT 0,
    error       text NOT NULL DEFAULT ''
);
CREATE INDEX backup_runs_status_idx ON backup_runs (status) WHERE status IN ('queued', 'running');
