-- «Мой план»: a personal board per user with own columns, independent of task statuses.
CREATE TABLE plan_stages (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code       text CHECK (code IN ('today', 'week', 'later')), -- built-in column: title comes from the locale
    title      text NOT NULL DEFAULT '',                          -- custom column title
    sort       integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX plan_stages_user_idx ON plan_stages (user_id, sort);

CREATE TABLE plan_items (
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_id  uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    stage_id uuid NOT NULL REFERENCES plan_stages(id) ON DELETE CASCADE,
    sort     integer NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, task_id)
);
