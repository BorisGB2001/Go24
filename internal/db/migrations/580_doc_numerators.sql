-- «Нумераторы» генератора документов: нумератор — отдельная запись (формат, начальный номер, шаг, длина,
-- обнуление по дням, месяцам или годам), один нумератор можно выбрать в нескольких шаблонах — общая нумерация.
CREATE TABLE doc_numerators (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    format     text NOT NULL DEFAULT '{NUMBER}' CHECK (length(format) BETWEEN 1 AND 100),
    start      bigint NOT NULL DEFAULT 1 CHECK (start >= 0),
    step       integer NOT NULL DEFAULT 1 CHECK (step BETWEEN 1 AND 1000),
    pad        smallint NOT NULL DEFAULT 0 CHECK (pad BETWEEN 0 AND 12),
    reset      text NOT NULL DEFAULT 'never' CHECK (reset IN ('never', 'day', 'month', 'year')),
    last       bigint,                          -- the last number given; NULL — none yet
    period     text NOT NULL DEFAULT '',        -- the period of the last number («2026-10» when reset by months)
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Каждый шаблон получает свой нумератор с прежним форматом и счётчиком.
ALTER TABLE crm_doc_templates ADD COLUMN numerator_id uuid REFERENCES doc_numerators(id);
CREATE TEMP TABLE tpl_num ON COMMIT DROP AS
    SELECT id AS template_id, gen_random_uuid() AS numerator_id, name, numerator, counter FROM crm_doc_templates;
INSERT INTO doc_numerators (id, name, format, last) SELECT numerator_id, name, numerator, nullif(counter, 0) FROM tpl_num;
UPDATE crm_doc_templates t SET numerator_id = n.numerator_id FROM tpl_num n WHERE n.template_id = t.id;
ALTER TABLE crm_doc_templates ALTER COLUMN numerator_id SET NOT NULL, DROP COLUMN numerator, DROP COLUMN counter;
CREATE INDEX crm_doc_templates_numerator ON crm_doc_templates (numerator_id);
