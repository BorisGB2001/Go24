-- «Доска почёта» (GO24DEV-719): employees an administrator or an HR officer put on the company's honor board, as in
-- the portal — why and for what days (to_day NULL — until taken off).
CREATE TABLE honors (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reason text NOT NULL DEFAULT '',
    from_day date NOT NULL,
    to_day date,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX honors_user ON honors (user_id);
