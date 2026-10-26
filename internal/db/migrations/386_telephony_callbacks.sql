-- «Обратный звонок»: a form on a site where a client leaves their number; go24 rings the employees
-- of the form, and the one who answers is connected with the client called through the form's external line.
-- token is the form's public address (/cb/{token}).
CREATE TABLE telephony_callbacks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token      text NOT NULL UNIQUE DEFAULT replace(gen_random_uuid()::text, '-', ''),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    trunk      uuid NOT NULL REFERENCES telephony_trunks(id) ON DELETE CASCADE,
    ring_users uuid[] NOT NULL DEFAULT '{}',
    ring_mode  text NOT NULL DEFAULT 'all' CHECK (ring_mode IN ('all', 'queue')),
    title      text NOT NULL DEFAULT '',
    text       text NOT NULL DEFAULT '',
    button     text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The clients' requests: calling — go24 asked Asterisk to call; later — out of the line's working hours (or Asterisk
-- did not answer), the employees were told to call back.
CREATE TABLE telephony_callback_requests (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    callback   uuid NOT NULL REFERENCES telephony_callbacks(id) ON DELETE CASCADE,
    number     text NOT NULL,
    name       text NOT NULL DEFAULT '',
    page       text NOT NULL DEFAULT '',
    status     text NOT NULL CHECK (status IN ('calling', 'later')),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX telephony_callback_requests_at ON telephony_callback_requests (callback, created_at DESC);
