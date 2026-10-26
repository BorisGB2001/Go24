-- Settings of local applications, as app.option.* and user.option.* (GO24DEV-585): the application's own
-- (user_id NULL, set by administrators) and each employee's; values are any JSON, go24.js gets them with the frame.
CREATE TABLE app_options (
    app_id  uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    user_id uuid REFERENCES users(id) ON DELETE CASCADE,
    name    text NOT NULL CHECK (name <> '' AND length(name) <= 128),
    value   jsonb NOT NULL
);
CREATE UNIQUE INDEX app_options_key ON app_options (app_id, coalesce(user_id, '00000000-0000-0000-0000-000000000000'::uuid), name);
