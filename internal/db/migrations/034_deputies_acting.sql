-- Deputies of a department head, and the person acting for someone who is away («исполняющий обязанности»)
-- with the powers given to them.
CREATE TABLE department_deputies (
    department_id uuid NOT NULL REFERENCES departments(id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (department_id, user_id)
);
ALTER TABLE absences
    ADD COLUMN acting_id     uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN acting_rights text[] NOT NULL DEFAULT '{}'; -- view | work | control | manage | notify
CREATE INDEX absences_acting_idx ON absences (acting_id) WHERE acting_id IS NOT NULL;
