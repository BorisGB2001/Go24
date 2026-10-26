-- «Пресеты фильтра» of the portal CRM lists: a person saves the filter of deals (of a smart process) or leads under a
-- name, and may make one of them the default — the list opens with it.
CREATE TABLE crm_filter_presets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope      text NOT NULL CHECK (scope IN ('deal', 'lead') OR scope LIKE 'type:%'), -- deals, leads, a smart process
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    query      text NOT NULL CHECK (length(query) <= 2000), -- the list's filter as a URL query (semantic=…&responsible=me)
    is_default boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX crm_filter_presets_name_idx ON crm_filter_presets (user_id, scope, lower(name));
CREATE UNIQUE INDEX crm_filter_presets_default_idx ON crm_filter_presets (user_id, scope) WHERE is_default;
