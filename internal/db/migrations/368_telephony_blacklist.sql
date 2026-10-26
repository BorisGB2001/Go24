-- Telephony's «Чёрный список»: calls from these numbers on the external lines are rejected («busy»).
-- key is the number's last 10 digits: +7…, 7… and 8… are one number.
CREATE TABLE telephony_blacklist (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    key        text NOT NULL UNIQUE,
    number     text NOT NULL,
    comment    text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
