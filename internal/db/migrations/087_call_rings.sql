-- Calls rung from a chat (GO24DEV-254): who calls, in which chat, audio or video, and how each person
-- rung answered; the ring is over once the caller is in the call with someone, gives up, or nobody answers in time.
CREATE TABLE call_rings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    caller_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_room  text NOT NULL,
    video      boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz
);

CREATE TABLE call_ring_people (
    ring_id uuid NOT NULL REFERENCES call_rings(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    answer  text NOT NULL DEFAULT '' CHECK (answer IN ('', 'accepted', 'declined', 'missed')),
    PRIMARY KEY (ring_id, user_id)
);
CREATE INDEX call_ring_people_user ON call_ring_people (user_id) WHERE answer = '';
