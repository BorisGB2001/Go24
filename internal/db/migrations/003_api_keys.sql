-- Personal API keys: token "go24_<key_id>_<secret>"; only SHA-256 of the secret is stored.
CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_id       text NOT NULL UNIQUE,      -- public part, safe to show and log
    name         text NOT NULL,
    secret_hash  bytea NOT NULL,
    scopes       text[] NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz,
    last_used_ip text NOT NULL DEFAULT ''
);
CREATE INDEX api_keys_user_idx ON api_keys (user_id);
