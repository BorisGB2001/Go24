-- «Корзина» of the portal CRM (GO24DEV-541): a deleted deal, lead, contact, company or smart process card is kept
-- as a snapshot of its rows (the card, its fields, products, deeds, contacts, requisites… and the links of other
-- cards to it) — restored from it, or gone for good by hand or after 30 days.
CREATE TABLE crm_recycle (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity         text NOT NULL CHECK (entity IN ('deal', 'lead', 'contact', 'company')),
    entity_id      uuid NOT NULL,
    type_id        uuid REFERENCES crm_types(id) ON DELETE SET NULL, -- a smart process card's type
    title          text NOT NULL DEFAULT '',
    responsible_id uuid,
    deleted_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    deleted_at     timestamptz NOT NULL DEFAULT now(),
    data           jsonb NOT NULL
);
CREATE INDEX crm_recycle_at_idx ON crm_recycle (deleted_at DESC);

-- «Восстановлено из корзины» in the card's timeline
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored'));
