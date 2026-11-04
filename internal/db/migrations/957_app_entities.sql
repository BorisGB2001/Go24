-- The data storage of applications, as entity.* (GO24DEV-979): an application keeps its own records in
-- entities (ENTITY — its code), sorted in sections, with properties of its own (string, number, file). The rights to
-- an entity are access codes — {"AU": "R", "U5": "W", "DR3": "X"}; administrators may everything.
CREATE TABLE app_entities (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id     uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    code       text NOT NULL CHECK (code ~ '^[A-Za-z0-9_]{1,13}$'),
    name       text NOT NULL CHECK (length(name) <= 255),
    access     jsonb NOT NULL DEFAULT '{"AU": "R"}',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (app_id, code)
);

CREATE TABLE app_entity_props (
    entity_id uuid NOT NULL REFERENCES app_entities(id) ON DELETE CASCADE,
    code      text NOT NULL CHECK (code ~ '^[A-Za-z0-9_]{1,50}$'),
    name      text NOT NULL CHECK (length(name) <= 255),
    type      char(1) NOT NULL CHECK (type IN ('S', 'N', 'F')),
    sort      int NOT NULL DEFAULT 500,
    PRIMARY KEY (entity_id, code)
);

CREATE TABLE app_entity_sections (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity_id   uuid NOT NULL REFERENCES app_entities(id) ON DELETE CASCADE,
    parent_id   bigint REFERENCES app_entity_sections(id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    code        text NOT NULL DEFAULT '' CHECK (length(code) <= 255),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 65536),
    sort        int NOT NULL DEFAULT 500,
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX app_entity_sections_entity ON app_entity_sections (entity_id);

-- props: the values of the properties by their codes, as text; a file property holds the id of its app_entity_files.
CREATE TABLE app_entity_items (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity_id      uuid NOT NULL REFERENCES app_entities(id) ON DELETE CASCADE,
    section_id     bigint REFERENCES app_entity_sections(id) ON DELETE CASCADE,
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    code           text NOT NULL DEFAULT '' CHECK (length(code) <= 255),
    active         boolean NOT NULL DEFAULT true,
    sort           int NOT NULL DEFAULT 500,
    active_from    timestamptz,
    active_to      timestamptz,
    preview_text   text NOT NULL DEFAULT '' CHECK (length(preview_text) <= 65536),
    detail_text    text NOT NULL DEFAULT '' CHECK (length(detail_text) <= 1048576),
    props          jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),
    created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX app_entity_items_entity ON app_entity_items (entity_id, sort, id);
CREATE INDEX app_entity_items_section ON app_entity_items (section_id);

-- Files of the items: PREVIEW_PICTURE, DETAIL_PICTURE and the file properties (slot «P:<code>»).
CREATE TABLE app_entity_files (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id bigint NOT NULL REFERENCES app_entity_items(id) ON DELETE CASCADE,
    slot    text NOT NULL,
    name    text NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    data    bytea NOT NULL,
    UNIQUE (item_id, slot)
);
