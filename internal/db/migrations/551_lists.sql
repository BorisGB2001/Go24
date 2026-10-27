-- «Списки» universal lists (GO24DEV-783): a list of an administrator's fields (as a template's: string, text,
-- number, date, date and time, yes/no, a select, an employee), its elements and their history; «Кто видит» and «Кто
-- добавляет и меняет» — people and departments (with their sub-departments), none — everybody. A business process
-- template may be of the elements of a list: it starts on an element added or changed, or from the element's card.
CREATE TABLE lists (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '',
    fields      jsonb NOT NULL DEFAULT '[]', -- [bp.Field]
    columns     jsonb NOT NULL DEFAULT '[]', -- the keys of the fields the table shows
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE list_rights (
    list_id       uuid NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    op            text NOT NULL CHECK (op IN ('read', 'write')),
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX list_rights_u ON list_rights (list_id, op, coalesce(user_id, department_id));

CREATE TABLE list_elements (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    list_id    uuid NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 500),
    values     jsonb NOT NULL DEFAULT '{}', -- {field key: value as bp keeps it}
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    changed_by uuid REFERENCES users(id) ON DELETE SET NULL,
    changed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX list_elements_list ON list_elements (list_id, created_at DESC);

-- the history of an element; the worker starts the templates of the list by it, as by the CRM timeline
CREATE TABLE list_log (
    id         bigserial PRIMARY KEY,
    element_id uuid NOT NULL REFERENCES list_elements(id) ON DELETE CASCADE,
    author_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    created    boolean NOT NULL,
    changes    jsonb NOT NULL DEFAULT '[]', -- [{"key", "old", "new"}]; "name" — the element's name
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX list_log_element ON list_log (element_id, id);

ALTER TABLE bp_templates DROP CONSTRAINT IF EXISTS bp_templates_entity_check;
ALTER TABLE bp_templates ADD CONSTRAINT bp_templates_entity_check CHECK (entity IN ('none', 'deal', 'lead', 'contact', 'company', 'file', 'list'));
ALTER TABLE bp_templates ADD COLUMN list_id uuid REFERENCES lists(id) ON DELETE CASCADE;
ALTER TABLE bp_templates ADD CONSTRAINT bp_templates_list CHECK ((entity = 'list') = (list_id IS NOT NULL));
-- the worker reads the lists' history after this event
ALTER TABLE bp_cursor ADD COLUMN list_id bigint NOT NULL DEFAULT 0;
