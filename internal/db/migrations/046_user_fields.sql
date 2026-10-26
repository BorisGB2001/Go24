-- «Пользовательские поля» of employees' profiles, like those of tasks (see 038).
CREATE TABLE user_fields (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind       text NOT NULL CHECK (kind IN ('string', 'number', 'date', 'bool', 'list', 'url')),
    options    text[] NOT NULL DEFAULT '{}',
    sort       int NOT NULL DEFAULT 100,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX user_fields_name_idx ON user_fields (lower(name));

CREATE TABLE user_field_values (
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    field_id uuid NOT NULL REFERENCES user_fields(id) ON DELETE CASCADE,
    value    text NOT NULL CHECK (length(value) BETWEEN 1 AND 2000),
    PRIMARY KEY (user_id, field_id)
);
CREATE INDEX user_field_values_field_idx ON user_field_values (field_id);
