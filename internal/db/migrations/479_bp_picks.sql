-- «Выбор сотрудника» of business processes picking in turn: whom each such step of a template picked last time.
CREATE TABLE bp_picks (
    template_id uuid NOT NULL REFERENCES bp_templates(id) ON DELETE CASCADE,
    step        int NOT NULL,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (template_id, step)
);
