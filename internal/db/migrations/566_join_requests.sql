-- «Быстрая регистрация» of «Настройки → Сотрудники»: a person who opened the registration link asks
-- to join; an administrator accepts (an invitation is made of the request — invite_id) or rejects it. The settings
-- themselves are in company_settings ('staff_settings').
CREATE TABLE join_requests (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email      text NOT NULL,
    name       text NOT NULL,
    ip         text NOT NULL DEFAULT '',
    state      text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'accepted', 'rejected')),
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_by uuid REFERENCES users(id) ON DELETE SET NULL,
    decided_at timestamptz,
    invite_id  uuid REFERENCES employee_invites(id) ON DELETE SET NULL
);
CREATE INDEX join_requests_pending ON join_requests (lower(email)) WHERE state = 'pending';
