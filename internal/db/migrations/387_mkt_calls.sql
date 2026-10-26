-- «Обзвон» of «Маркетинг»: a list of calls to the cards of a CRM segment with a phone — started, it
-- plans a «Звонок» deed in each card for its responsible or the chosen employees in turn; its progress is the deeds done.
CREATE TABLE mkt_calls (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    segment_id  uuid REFERENCES mkt_segments(id) ON DELETE SET NULL,
    script      text NOT NULL DEFAULT '',
    deadline    timestamptz,
    assign      text NOT NULL DEFAULT 'owner' CHECK (assign IN ('owner', 'staff')),
    staff       uuid[] NOT NULL DEFAULT '{}',
    status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'started')),
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mkt_call_items (
    id             bigserial PRIMARY KEY,
    call_id        uuid NOT NULL REFERENCES mkt_calls(id) ON DELETE CASCADE,
    entity         text NOT NULL CHECK (entity IN ('contact', 'company', 'lead')),
    entity_id      uuid NOT NULL,
    name           text NOT NULL DEFAULT '',
    phone          text NOT NULL DEFAULT '',
    responsible_id uuid REFERENCES users(id) ON DELETE SET NULL,
    activity_id    uuid REFERENCES crm_activities(id) ON DELETE SET NULL
);
CREATE INDEX mkt_call_items_call ON mkt_call_items (call_id, id);
