-- «Группы очередей»: employees a call waits for in a queue (Asterisk's app_queue) — all at once, in turn
-- or evenly (the one longest without a call first); ring — how long one employee rings, wait — how long the client
-- waits in the queue at most; music — what the client hears while waiting (WAV for Asterisk; NULL — the ringing).
-- A line rings a group instead of its employees (ring_group); a voice menu's item may connect with a group.
CREATE TABLE telephony_groups (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    users      uuid[] NOT NULL DEFAULT '{}',
    strategy   text NOT NULL DEFAULT 'all' CHECK (strategy IN ('all', 'linear', 'evenly')),
    ring       integer NOT NULL DEFAULT 20 CHECK (ring BETWEEN 5 AND 120),
    wait       integer NOT NULL DEFAULT 120 CHECK (wait BETWEEN 10 AND 1800),
    music      bytea,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE telephony_trunks ADD COLUMN ring_group uuid REFERENCES telephony_groups(id) ON DELETE SET NULL;
