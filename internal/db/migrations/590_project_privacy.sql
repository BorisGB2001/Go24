-- «Конфиденциальность» of a portal group: open — every employee sees it in «Все группы» and joins at once; closed —
-- seen by everyone, joined on a request a moderator accepts; secret — seen by its members only (as every project was
-- before, so they all stay secret). Collabs have their own «Тип коллаба».
ALTER TABLE projects ADD COLUMN privacy text NOT NULL DEFAULT 'secret' CHECK (privacy IN ('open', 'closed', 'secret'));

-- Requests to join a closed group, waiting for a moderator.
CREATE TABLE project_join_requests (
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message    text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX project_join_requests_user_idx ON project_join_requests (user_id);
