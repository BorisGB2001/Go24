-- «Приложения → Разработчикам → Локальное приложение»: an administrator registers an application (its
-- page and install handler, a menu item, the REST sections it may use) and gets its code (client_id) and key
-- (client_secret). The application acts in REST for an employee with OAuth 2.0 tokens: the access token lives an hour,
-- the refresh token 28 days; both are stored as SHA-256 only. application_token goes with the events go24 sends the
-- application, so it knows they are from this go24.
CREATE TABLE apps (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    handler_url       text NOT NULL DEFAULT '' CHECK (handler_url = '' OR handler_url ~ '^https?://'),
    install_url       text NOT NULL DEFAULT '' CHECK (install_url = '' OR install_url ~ '^https?://'),
    menu_title        text NOT NULL DEFAULT '',
    api_only          boolean NOT NULL DEFAULT false,
    scopes            text[] NOT NULL DEFAULT '{}',
    client_id         text NOT NULL UNIQUE,
    client_secret     text NOT NULL,
    application_token text NOT NULL,
    installed_at      timestamptz,
    install_status    int,
    install_error     text NOT NULL DEFAULT '',
    created_by        uuid NOT NULL REFERENCES users(id),
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- token_id is the public part of both tokens (go24at_<token_id>_<secret>, go24rt_<token_id>_<secret>); a refresh
-- gives the row new secrets.
CREATE TABLE app_tokens (
    token_id        text PRIMARY KEY,
    app_id          uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    access_hash     bytea NOT NULL,
    refresh_hash    bytea NOT NULL,
    access_expires  timestamptz NOT NULL,
    refresh_expires timestamptz NOT NULL,
    last_used_at    timestamptz,
    last_used_ip    text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX app_tokens_app_idx ON app_tokens (app_id, user_id);

-- An authorization code of /oauth/authorize, exchanged once for tokens within a minute.
CREATE TABLE app_codes (
    code_hash  bytea PRIMARY KEY,
    app_id     uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);
