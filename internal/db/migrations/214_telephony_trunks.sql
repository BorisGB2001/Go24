-- «Внешние линии» of the telephony SIP connector (GO24DEV-505): a provider's SIP trunk — its server,
-- login and password (sealed with go24's key, as the lines'), the line's own number (shown to whom one calls, and the
-- number clients call). Incoming calls ring the employees of ring_users: all at once or by turns; outgoing ones go
-- through the first line (by position) the employee may call out by: anyone (out_all) or out_users.
CREATE TABLE telephony_trunks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    host       text NOT NULL, -- server[:port]
    username   text NOT NULL,
    secret     bytea NOT NULL,
    auth_user  text NOT NULL DEFAULT '', -- when the provider's login for the password differs
    number     text NOT NULL,
    register   boolean NOT NULL DEFAULT true,
    ring_mode  text NOT NULL DEFAULT 'all' CHECK (ring_mode IN ('all', 'queue')),
    ring_users uuid[] NOT NULL DEFAULT '{}',
    out_all    boolean NOT NULL DEFAULT true,
    out_users  uuid[] NOT NULL DEFAULT '{}',
    position   integer NOT NULL DEFAULT 0,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- the external line a call went through ("" — between employees)
ALTER TABLE telephony_calls ADD COLUMN line text NOT NULL DEFAULT '';
