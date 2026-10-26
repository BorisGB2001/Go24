-- «Карточка звонка»: the employee's comment on a call, written in the softphone while talking or after.
-- The call reaches the log only when it ends, so a comment written before waits in telephony_notes (the employee, the
-- outside number by its last 10 digits) and goes to the call when Asterisk reports it.
ALTER TABLE telephony_calls ADD COLUMN comment text NOT NULL DEFAULT '';

CREATE TABLE telephony_notes (
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    number     text NOT NULL,
    comment    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, number)
);
