-- «Открытые события» of the calendar (GO24DEV-619): categories of events open to every employee, who joins
-- one without an invitation; the new events of a category come as a notification unless it is muted.
CREATE TABLE event_categories (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    closed      boolean NOT NULL DEFAULT false, -- only its creator (and admins) add events
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE event_category_mutes (
    category_id uuid NOT NULL REFERENCES event_categories(id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (category_id, user_id)
);

ALTER TABLE events ADD COLUMN category_id uuid REFERENCES event_categories(id) ON DELETE SET NULL;
CREATE INDEX events_category ON events (category_id, starts_at) WHERE category_id IS NOT NULL;
