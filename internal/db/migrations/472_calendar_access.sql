-- Access to one's calendar for colleagues by name, as «Доступ» of a calendar (GO24DEV-710): over what
-- all colleagues see (users.calendar_share) the owner gives a person 'view' — the meetings in full — or 'edit' —
-- they keep the calendar for the owner (a secretary): make, change and delete the owner's meetings, answer the
-- owner's invitations. Private meetings stay only the busy time to them.
CREATE TABLE calendar_access (
    owner_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    level      text NOT NULL CHECK (level IN ('view', 'edit')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, user_id)
);
CREATE INDEX calendar_access_user ON calendar_access (user_id);
