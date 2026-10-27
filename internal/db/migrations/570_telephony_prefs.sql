-- The employee's own telephony settings (profile → «Телефония», GO24DEV-736): the external line their
-- calls out go by (NULL — the first one they may call by) and forwarding to their mobile: never, while neither their
-- SIP phone nor the softphone is online, when they did not answer within forward_after seconds, or always. The mobile
-- goes out by the line of the call (a call from an external line) or by the employee's line out (a colleague's call).
CREATE TABLE telephony_prefs (
    user_id       uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    out_trunk     uuid REFERENCES telephony_trunks (id) ON DELETE SET NULL,
    forward       text NOT NULL DEFAULT '' CHECK (forward IN ('', 'offline', 'noanswer', 'always')),
    forward_after int NOT NULL DEFAULT 20,
    forward_to    text NOT NULL DEFAULT '',
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- The call went to the employee's mobile (its number; "" — not forwarded).
ALTER TABLE telephony_calls ADD COLUMN mobile text NOT NULL DEFAULT '';
