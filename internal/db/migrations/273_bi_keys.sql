-- Keys of «BI-аналитика» for external BI (Power BI, Looker Studio, DataLens), as «Подключение внешних BI»:
-- an administrator issues one; it reads the rows of the data sets (all, or those listed) with the rights of
-- the administrator in admin mode while they stay one. Token "go24bi_<key_id>_<secret>": only SHA-256 of the secret is stored.
CREATE TABLE bi_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    key_id       text NOT NULL UNIQUE,
    name         text NOT NULL,
    secret_hash  bytea NOT NULL,
    datasets     text[] NOT NULL DEFAULT '{}',
    created_by   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz,
    last_used_at timestamptz,
    last_used_ip text NOT NULL DEFAULT '',
    uses         bigint NOT NULL DEFAULT 0
);
