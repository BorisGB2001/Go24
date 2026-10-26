-- «Пользовательские поля» tasks: an administrator defines the fields, every task may have a value for each.
CREATE TABLE task_fields (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind       text NOT NULL CHECK (kind IN ('string', 'number', 'date', 'bool', 'list', 'url')),
    options    text[] NOT NULL DEFAULT '{}', -- the values of a list
    sort       int NOT NULL DEFAULT 100,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX task_fields_name_idx ON task_fields (lower(name));

CREATE TABLE task_field_values (
    task_id  uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    field_id uuid NOT NULL REFERENCES task_fields(id) ON DELETE CASCADE,
    value    text NOT NULL CHECK (length(value) BETWEEN 1 AND 2000),
    PRIMARY KEY (task_id, field_id)
);
CREATE INDEX task_field_values_field_idx ON task_field_values (field_id);
