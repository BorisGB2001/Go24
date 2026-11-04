-- «Переход по ссылке» (a trigger): the trigger's address in the letters robots write to the client becomes
-- a tracked link of the card; the client's following it moves the card.
CREATE TABLE crm_trigger_links (
    token      text PRIMARY KEY,
    entity     text NOT NULL CHECK (entity IN ('deal', 'lead')),
    entity_id  uuid NOT NULL,
    url        text NOT NULL,
    author_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- who sent it: the history's line is theirs
    clicks     int NOT NULL DEFAULT 0,
    clicked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (entity, entity_id, url)
);
ALTER TABLE crm_triggers DROP CONSTRAINT crm_triggers_kind_check;
ALTER TABLE crm_triggers ADD CONSTRAINT crm_triggers_kind_check
    CHECK (kind IN ('mail', 'form', 'openline', 'activity', 'invoice_paid', 'quote_accepted',
        'booking', 'booking_confirmed', 'booking_came', 'booking_no_show', 'booking_cancelled', 'booking_moved',
        'call', 'call_missed', 'field', 'webhook', 'doc_viewed', 'doc_signed', 'task', 'link'));
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft', 'repeat', 'bound', 'unbound', 'doc_viewed', 'doc_signed', 'link_clicked'));
