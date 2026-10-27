-- «Состояние системы», as «Проверка системы»: every running copy of go24 (pages, worker) says it is alive
-- once a minute; a copy not seen for a few minutes is shown as stopped.
CREATE TABLE system_nodes (
    id         text PRIMARY KEY, -- host and role: one row per container
    role       text NOT NULL,    -- web, worker, all
    host       text NOT NULL,
    version    text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL,
    seen_at    timestamptz NOT NULL DEFAULT now()
);
