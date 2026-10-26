-- Task tracker core: projects, tasks with portal-style roles and statuses, checklists, comments, history.

CREATE TABLE projects (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    key         text NOT NULL UNIQUE CHECK (key ~ '^[A-Z][A-Z0-9]{1,9}$'), -- task key prefix: GO-1, GO-2…
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    parent_id   uuid REFERENCES projects(id) ON DELETE SET NULL,
    owner_id    uuid NOT NULL REFERENCES users(id),
    archived    boolean NOT NULL DEFAULT false,
    task_seq    integer NOT NULL DEFAULT 0, -- last issued task number
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- owner/manager manage the project and its members; member works with tasks; viewer only reads.
CREATE TABLE project_members (
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('owner', 'manager', 'member', 'viewer')),
    added_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX project_members_user_idx ON project_members (user_id);

-- Statuses mirror the portal: 2 pending, 3 in_progress, 4 awaiting_control, 5 completed, 6 deferred.
CREATE TABLE tasks (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    number           integer NOT NULL,
    title            text NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    description      text NOT NULL DEFAULT '',
    status           text NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'in_progress', 'awaiting_control', 'completed', 'deferred')),
    priority         smallint NOT NULL DEFAULT 1 CHECK (priority BETWEEN 0 AND 2), -- 0 low, 1 normal, 2 high
    creator_id       uuid NOT NULL REFERENCES users(id),   -- постановщик
    responsible_id   uuid NOT NULL REFERENCES users(id),   -- исполнитель
    parent_id        uuid REFERENCES tasks(id) ON DELETE SET NULL,
    requires_control boolean NOT NULL DEFAULT false,       -- принять работу после завершения
    deadline         timestamptz,
    start_plan       date,
    end_plan         date,
    closed_at        timestamptz,
    closed_by        uuid REFERENCES users(id),
    version          integer NOT NULL DEFAULT 1,           -- optimistic locking
    legacy_id        integer UNIQUE,                       -- b_tasks.ID, filled by the importer
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, number)
);
CREATE INDEX tasks_responsible_idx ON tasks (responsible_id, status);
CREATE INDEX tasks_creator_idx ON tasks (creator_id, status);
CREATE INDEX tasks_parent_idx ON tasks (parent_id);
CREATE INDEX tasks_project_status_idx ON tasks (project_id, status);

-- соисполнители (accomplice) и наблюдатели (auditor)
CREATE TABLE task_participants (
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role    text NOT NULL CHECK (role IN ('accomplice', 'auditor')),
    PRIMARY KEY (task_id, user_id, role)
);
CREATE INDEX task_participants_user_idx ON task_participants (user_id, role);

CREATE TABLE task_checklist_items (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    title      text NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    done       boolean NOT NULL DEFAULT false,
    sort       integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL REFERENCES users(id),
    done_by    uuid REFERENCES users(id),
    done_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX task_checklist_task_idx ON task_checklist_items (task_id, sort);

CREATE TABLE task_comments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    author_id  uuid NOT NULL REFERENCES users(id),
    body       text NOT NULL CHECK (length(body) BETWEEN 1 AND 20000),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX task_comments_task_idx ON task_comments (task_id, created_at);

CREATE TABLE task_history (
    id         bigserial PRIMARY KEY,
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    actor_id   uuid NOT NULL REFERENCES users(id),
    field      text NOT NULL,
    old_value  text NOT NULL DEFAULT '',
    new_value  text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX task_history_task_idx ON task_history (task_id, created_at);
