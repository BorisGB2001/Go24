-- The planner of the calendar (GO24DEV-233): how a meeting shows the organizer's time to others —
-- busy, free (not shown as busy), tentative, absent — and a private meeting (only its people see what it is; others
-- see «busy»).
ALTER TABLE events
    ADD COLUMN accessibility text NOT NULL DEFAULT 'busy' CHECK (accessibility IN ('busy', 'free', 'tentative', 'absent')),
    ADD COLUMN private       boolean NOT NULL DEFAULT false;
