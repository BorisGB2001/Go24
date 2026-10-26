-- «Открытые линии»: who sees which dialogues, as the roles. An operator — their own dialogues and the
-- unassigned ones of their lines' queue; a department's head — also those of their people; the portal's administrators
-- and the lines' heads listed here — every dialogue and the statistics of everyone.
CREATE TABLE ol_heads (
    user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    added_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    added_at   timestamptz NOT NULL DEFAULT now()
);
