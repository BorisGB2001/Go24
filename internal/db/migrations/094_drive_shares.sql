-- «Общий доступ» of «Диск»: a folder of «Мой диск» opened to an employee or a department (with its
-- sub-departments) for reading, editing or full access; they find it in «Общие со мной».
CREATE TABLE drive_shares (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    folder_id     uuid NOT NULL REFERENCES drive_folders(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    access        text NOT NULL CHECK (access IN ('read', 'edit', 'full')),
    created_by    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX drive_shares_user_idx ON drive_shares (folder_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX drive_shares_dept_idx ON drive_shares (folder_id, department_id) WHERE department_id IS NOT NULL;
CREATE INDEX drive_shares_user_lookup_idx ON drive_shares (user_id);
CREATE INDEX drive_shares_dept_lookup_idx ON drive_shares (department_id);
