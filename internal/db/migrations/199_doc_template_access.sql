-- «Кому доступен» of a document template (GO24DEV-518): the people and the departments (with their
-- sub-departments) who may make documents by it; no rows — everybody.
CREATE TABLE crm_doc_template_access (
    template_id   uuid NOT NULL REFERENCES crm_doc_templates(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX crm_doc_template_access_u ON crm_doc_template_access (template_id, coalesce(user_id, department_id));
