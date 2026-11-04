-- Push notifications on the phone (GO24DEV-743), as the portal mobile app's: the company's VAPID key is the token of
-- the «push» channel in notify_bots; an employee's notify_links row of the channel (chat 'devices') keeps the kinds
-- they want and the last notification looked at, the devices themselves are here — one row a browser subscription.

-- A device of an employee: where its browser's push service takes the messages (endpoint) and the keys the messages
-- are encrypted with for that browser (p256dh, auth — RFC 8291); label — the device as the browser names it.
CREATE TABLE push_devices (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    endpoint   text NOT NULL UNIQUE,
    p256dh     text NOT NULL,
    auth       text NOT NULL,
    label      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    used_at    timestamptz
);
CREATE INDEX push_devices_user_idx ON push_devices (user_id);
