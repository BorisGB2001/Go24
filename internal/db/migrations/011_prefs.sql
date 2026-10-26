-- Personal UI settings (task list columns and the like), kept on the server so they follow the user
-- to any browser grid options.
CREATE TABLE user_prefs (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key        text NOT NULL,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);
