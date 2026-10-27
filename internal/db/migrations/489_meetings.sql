-- «Собрания и планёрки» (GO24DEV-732): a meeting with its organizer, the one who keeps the minutes and
-- the members, an agenda of items (and sub-items) with the reporter, the result and the tasks set on it; a series of
-- planning meetings goes by the previous one (prev_id), the meeting of the calendar is event_id.
CREATE TABLE meetings (
    id bigserial PRIMARY KEY,
    title text NOT NULL,
    description text NOT NULL DEFAULT '',
    place text NOT NULL DEFAULT '',
    starts_at timestamptz NOT NULL,
    minutes int NOT NULL DEFAULT 60,
    status text NOT NULL DEFAULT 'prepare', -- prepare | action | done
    owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    keeper_id uuid REFERENCES users(id) ON DELETE SET NULL,
    prev_id bigint REFERENCES meetings(id) ON DELETE SET NULL,
    event_id uuid REFERENCES events(id) ON DELETE SET NULL,
    protocol text NOT NULL DEFAULT '',
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX meetings_starts ON meetings (starts_at);
CREATE INDEX meetings_prev ON meetings (prev_id);

CREATE TABLE meeting_members (
    meeting_id bigint NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (meeting_id, user_id)
);
CREATE INDEX meeting_members_user ON meeting_members (user_id);

CREATE TABLE meeting_items (
    id bigserial PRIMARY KEY,
    meeting_id bigint NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    parent_id bigint REFERENCES meeting_items(id) ON DELETE CASCADE,
    sort int NOT NULL DEFAULT 0,
    title text NOT NULL,
    reporter_id uuid REFERENCES users(id) ON DELETE SET NULL,
    minutes int NOT NULL DEFAULT 0,
    result text NOT NULL DEFAULT ''
);
CREATE INDEX meeting_items_meeting ON meeting_items (meeting_id);

CREATE TABLE meeting_item_tasks (
    item_id bigint NOT NULL REFERENCES meeting_items(id) ON DELETE CASCADE,
    task_id uuid NOT NULL REFERENCES tasks_all(id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, task_id)
);
