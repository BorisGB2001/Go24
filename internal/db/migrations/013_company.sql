-- «Сотрудники»: the employee profile (b_user fields) and the company structure
-- (departments with sub-departments and a head, b_iblock_section of the departments iblock + UF_HEAD).
ALTER TABLE users
    ADD COLUMN last_name    text NOT NULL DEFAULT '',
    ADD COLUMN first_name   text NOT NULL DEFAULT '',
    ADD COLUMN second_name  text NOT NULL DEFAULT '', -- отчество
    ADD COLUMN position     text NOT NULL DEFAULT '', -- должность
    ADD COLUMN work_phone   text NOT NULL DEFAULT '',
    ADD COLUMN mobile_phone text NOT NULL DEFAULT '',
    ADD COLUMN birthday     date,
    ADD COLUMN about        text NOT NULL DEFAULT '',
    ADD COLUMN active       boolean NOT NULL DEFAULT true; -- false = уволен: hidden from pickers, cannot sign in

CREATE TABLE departments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id  uuid REFERENCES departments(id) ON DELETE RESTRICT, -- NULL only for the company itself
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    head_id    uuid REFERENCES users(id) ON DELETE SET NULL,      -- руководитель
    sort       integer NOT NULL DEFAULT 500,
    legacy_id  integer UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX departments_root_idx ON departments ((parent_id IS NULL)) WHERE parent_id IS NULL;
CREATE INDEX departments_parent_idx ON departments (parent_id, sort);

-- An employee may belong to several departments, like UF_DEPARTMENT.
CREATE TABLE user_departments (
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid NOT NULL REFERENCES departments(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, department_id)
);
CREATE INDEX user_departments_dept_idx ON user_departments (department_id);
