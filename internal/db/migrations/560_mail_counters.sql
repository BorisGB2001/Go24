-- «Предложить новое время», as in Outlook (iTIP COUNTER): the time the attendee proposed to the organizer (until the
-- organizer declines it or sends the meeting again).
ALTER TABLE mail_invites
    ADD COLUMN proposed_start timestamptz,
    ADD COLUMN proposed_end   timestamptz;

-- The organizer's decision on a proposal of a new time that came in a letter: accepted (the meeting moved) or declined.
CREATE TABLE mail_counters (
    message_id uuid PRIMARY KEY REFERENCES mail_messages(id) ON DELETE CASCADE,
    status     text NOT NULL CHECK (status IN ('accepted', 'declined')),
    updated_at timestamptz NOT NULL DEFAULT now()
);
