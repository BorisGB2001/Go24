-- «Подключить к своему Диску»: a folder shared with the user or a project's drive shown in their «Мой диск»
CREATE TABLE drive_connections (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    folder_id  uuid REFERENCES drive_folders (id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((folder_id IS NULL) <> (project_id IS NULL)),
    UNIQUE (user_id, folder_id),
    UNIQUE (user_id, project_id)
);
