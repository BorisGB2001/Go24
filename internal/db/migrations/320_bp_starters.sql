-- «Кто может запускать» a business process template, as the rights to start (GO24DEV-392): the people and
-- the departments (with their sub-departments) who start it by hand; no rows — everybody. Administrators always may;
-- the autostart of a CRM card does not look at it.
CREATE TABLE bp_template_starters (
    template_id   uuid NOT NULL REFERENCES bp_templates(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX bp_template_starters_u ON bp_template_starters (template_id, coalesce(user_id, department_id));
