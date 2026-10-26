-- Subscription to one's calendar from Outlook, Google Calendar or a phone (GO24DEV-238), as the calendar sync of
-- the portal: a secret link to an iCalendar feed; issuing it again makes the old link stop working.
CREATE TABLE calendar_feeds (
    user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    token      text NOT NULL UNIQUE,
    tasks      boolean NOT NULL DEFAULT true, -- with the deadlines of one's tasks
    created_at timestamptz NOT NULL DEFAULT now()
);
