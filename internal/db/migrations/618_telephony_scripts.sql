-- CoPilot's call assessment: scripts of what a conversation should have, the AI scores a call against
-- one by the record's text (GO24DEV-816).
CREATE TABLE telephony_scripts (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    body       text NOT NULL,
    calls      text NOT NULL DEFAULT 'all', -- all | in | out
    auto       boolean NOT NULL DEFAULT true,
    low        int NOT NULL DEFAULT 50,     -- a score below it is low: the employee's managers are told
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE telephony_calls
    ADD COLUMN score int,
    ADD COLUMN score_script uuid REFERENCES telephony_scripts(id) ON DELETE SET NULL,
    ADD COLUMN score_name text NOT NULL DEFAULT '',
    ADD COLUMN score_text text NOT NULL DEFAULT '';
