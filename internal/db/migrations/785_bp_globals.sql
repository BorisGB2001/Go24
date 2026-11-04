-- «Глобальные константы» and «Глобальные переменные» of workflows (GO24DEV-894): values every template
-- reads as {global.KEY}; a step changes a variable for all processes, a constant only the administrator.
CREATE TABLE bp_globals (
    key        text PRIMARY KEY,
    label      text NOT NULL,
    kind       text NOT NULL,
    options    text[] NOT NULL DEFAULT '{}',
    is_var     boolean NOT NULL DEFAULT false,
    value      text NOT NULL DEFAULT '',
    position   int NOT NULL DEFAULT 0,
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
