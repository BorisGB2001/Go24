-- «Пресеты фильтра» of the portal task list: a person saves the filter they use as a named preset.
CREATE TABLE task_filter_presets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    query      text NOT NULL CHECK (length(query) <= 2000), -- the list's URL query (status=…&responsible=…)
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX task_filter_presets_name_idx ON task_filter_presets (user_id, lower(name));
