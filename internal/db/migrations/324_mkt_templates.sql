-- «Шаблоны писем» of «Маркетинг»: letters saved to start campaigns from (the ready-made ones are files).
CREATE TABLE mkt_templates (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    subject    text NOT NULL DEFAULT '',
    body       text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
