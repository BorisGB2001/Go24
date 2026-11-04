-- Fields of a template of the document generator, as b_documentgenerator_field (GO24DEV-1007): the
-- title «Изменить значения» shows, the value by default (when the card has none), required (no document without a
-- value) and «Скрывать строку, если пусто» (the paragraph or the table row of an empty field goes).
CREATE TABLE crm_doc_template_fields (
    template_id uuid NOT NULL REFERENCES crm_doc_templates(id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    title       text NOT NULL DEFAULT '' CHECK (length(title) <= 200),
    value       text NOT NULL DEFAULT '' CHECK (length(value) <= 2000),
    required    boolean NOT NULL DEFAULT false,
    hide_row    boolean NOT NULL DEFAULT false,
    PRIMARY KEY (template_id, name)
);
