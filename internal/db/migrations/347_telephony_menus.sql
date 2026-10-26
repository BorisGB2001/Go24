-- Voice menus (IVR), as «Голосовое меню» (GO24DEV-608): the greeting (8 kHz mono WAV) is played, the
-- client presses a key and the call goes by the item of that key (items: [{digit, act, users, mode, to, menu}] — act
-- ring | forward | voicemail | menu | repeat | hangup); nothing pressed after the menu was played `repeats` times —
-- by `timeout` (an item without a digit). direct: the client may dial an employee's inner number right away.
-- An external line's calls go to its menu (in the working hours) instead of ringing its employees.
CREATE TABLE telephony_menus (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    greeting   bytea,
    wait       integer NOT NULL DEFAULT 5 CHECK (wait BETWEEN 1 AND 30), -- seconds to wait for a key
    repeats    integer NOT NULL DEFAULT 2 CHECK (repeats BETWEEN 1 AND 5),
    direct     boolean NOT NULL DEFAULT true,
    items      jsonb NOT NULL DEFAULT '[]',
    timeout    jsonb NOT NULL DEFAULT '{"act": "hangup"}',
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE telephony_trunks ADD COLUMN menu uuid REFERENCES telephony_menus(id) ON DELETE SET NULL;
