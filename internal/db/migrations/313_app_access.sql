-- «Кому доступно» of a local application (GO24DEV-582): the people and the departments (with their
-- sub-departments) who see it in «Приложения» and the left menu, open its page and act through it in REST; no rows —
-- everybody.
CREATE TABLE app_access (
    app_id        uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX app_access_u ON app_access (app_id, coalesce(user_id, department_id));
