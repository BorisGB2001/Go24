-- Connections of AI providers, as CoPilot but with one's own models (GO24DEV-480): the company's (owner_id
-- NULL, kept by the administrators) and personal ones of an employee. The key is sealed (AES-GCM), only its last four
-- characters are shown. One connection of each owner is the default; ai_choice is the one an employee picked to use.
CREATE TABLE ai_connections (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    uuid REFERENCES users(id) ON DELETE CASCADE,
    name        text NOT NULL,
    provider    text NOT NULL,
    base_url    text NOT NULL DEFAULT '',
    model       text NOT NULL DEFAULT '',
    key_sealed  bytea,
    key_tail    text NOT NULL DEFAULT '',
    is_default  boolean NOT NULL DEFAULT false,
    checked_at  timestamptz,
    check_error text NOT NULL DEFAULT '',
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_connections_owner ON ai_connections (owner_id);
CREATE TABLE ai_choice (
    user_id       uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    connection_id uuid NOT NULL REFERENCES ai_connections(id) ON DELETE CASCADE
);
