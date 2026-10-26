-- Answers to a page's background form that go24 did not mean (a proxy's 502, a crash): the page reports them, so
-- the cause can be found later — the container's own log goes with it at every deploy. Kept 30 days.
CREATE TABLE client_errors (
    id         bigserial PRIMARY KEY,
    user_id    uuid REFERENCES users(id) ON DELETE SET NULL,
    page       text NOT NULL DEFAULT '',
    url        text NOT NULL DEFAULT '',
    status     int NOT NULL DEFAULT 0,
    body       text NOT NULL DEFAULT '',
    agent      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX client_errors_created_idx ON client_errors (created_at);
