-- «Журнал событий» of «Настройки → Безопасность» (GO24DEV-534): sign-ins and sign-outs, refused
-- sign-ins, the admin mode, wrong API keys, keys and webhooks made and removed, changes of the general settings,
-- employees dismissed and returned. name is the person as of the event (kept when the user is gone); target_id — the
-- employee the event is about. Kept company_settings 'audit_days' days (180 by default).
CREATE TABLE audit_log (
    id        bigserial PRIMARY KEY,
    at        timestamptz NOT NULL DEFAULT now(),
    kind      text NOT NULL,
    user_id   uuid REFERENCES users(id) ON DELETE SET NULL,
    name      text NOT NULL DEFAULT '',
    ip        text NOT NULL DEFAULT '',
    agent     text NOT NULL DEFAULT '',
    target    text NOT NULL DEFAULT '',
    target_id uuid REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX audit_log_at ON audit_log (at DESC);
CREATE INDEX audit_log_user ON audit_log (user_id, at DESC);
