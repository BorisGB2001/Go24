-- «Детализация звонков» (GO24DEV-503): every call of go24's PBX. Asterisk tells go24 about a call
-- when it ends (its hangup handler); uniqueid is Asterisk's, so a call told twice is kept once. The employees on
-- both ends are found by their inner numbers at the time of the call. record is the file of the conversation in
-- the volume of recordings ("" — not recorded): recording is the administrators' setting
-- company_settings 'telephony_record'.
CREATE TABLE telephony_calls (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    uniqueid    text NOT NULL UNIQUE,
    from_number text NOT NULL,
    to_number   text NOT NULL,
    from_user   uuid REFERENCES users(id) ON DELETE SET NULL,
    to_user     uuid REFERENCES users(id) ON DELETE SET NULL,
    started_at  timestamptz NOT NULL,
    answered_at timestamptz,
    seconds     integer NOT NULL DEFAULT 0, -- talked
    status      text NOT NULL, -- answered, busy, noanswer, cancel, failed
    record      text NOT NULL DEFAULT ''
);
CREATE INDEX telephony_calls_started ON telephony_calls (started_at DESC);
CREATE INDEX telephony_calls_from ON telephony_calls (from_user, started_at DESC);
CREATE INDEX telephony_calls_to ON telephony_calls (to_user, started_at DESC);
