-- «Календарь»: meetings in a personal, project (group) or company calendar, with invitations that people
-- accept or decline. Task deadlines are shown in the calendar too (read from tasks, not stored here).
CREATE TABLE events (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id  uuid REFERENCES projects(id) ON DELETE CASCADE, -- a project's calendar (its members see it)
    company     boolean NOT NULL DEFAULT false,                 -- the company calendar (everyone sees it)
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    description text NOT NULL DEFAULT '',
    location    text NOT NULL DEFAULT '',
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL CHECK (ends_at >= starts_at),
    all_day     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX events_range_idx ON events (starts_at, ends_at);

CREATE TABLE event_attendees (
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status   text NOT NULL DEFAULT 'invited' CHECK (status IN ('invited', 'accepted', 'declined')),
    PRIMARY KEY (event_id, user_id)
);
CREATE INDEX event_attendees_user_idx ON event_attendees (user_id);

-- invitations and answers come to the bell
ALTER TABLE notifications ADD COLUMN event_id uuid REFERENCES events(id) ON DELETE CASCADE;
