-- «Поиск дубликатов» of the CRM: the pairs of cards marked «Не дубликаты», no longer shown together (a < b).
CREATE TABLE crm_dup_ignored (
    entity     text NOT NULL CHECK (entity IN ('contact', 'company', 'lead')),
    a          uuid NOT NULL,
    b          uuid NOT NULL,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entity, a, b),
    CHECK (a < b)
);
