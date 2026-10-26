-- Sessions of employees, as «Безопасность» of the profile (GO24DEV-460): each sign-in is a row, its id goes
-- into the session cookie; a session ended here (by the employee or an administrator) is refused at the next request.
-- sessions_after: «Выйти на всех устройствах» — cookies made before sessions were kept (no id) are refused too.
CREATE TABLE user_sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    seen_at    timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    ip         text NOT NULL DEFAULT '',
    agent      text NOT NULL DEFAULT '',
    ended_at   timestamptz
);
CREATE INDEX user_sessions_user ON user_sessions (user_id, seen_at DESC);

ALTER TABLE users ADD COLUMN sessions_after timestamptz;
