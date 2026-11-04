-- «Ожидание» of the portal CRM timeline: wait N days or weeks after today or before a date of the card, with a
-- comment. A card has one open wait; it ends by hand («Завершить»), by a new wait, or by itself on its day.
CREATE TABLE crm_waits (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity      text NOT NULL CHECK (entity IN ('company', 'contact', 'deal', 'lead')),
    entity_id   uuid NOT NULL,
    until       timestamptz NOT NULL,
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 10000),
    created_by  uuid NOT NULL REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    done_at     timestamptz,
    done_by     uuid REFERENCES users(id) -- NULL with done_at: the wait was over by itself
);
CREATE UNIQUE INDEX crm_waits_open_idx ON crm_waits (entity, entity_id) WHERE done_at IS NULL;
CREATE INDEX crm_waits_due_idx ON crm_waits (until) WHERE done_at IS NULL;

CREATE FUNCTION crm_waits_gone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM crm_waits WHERE entity = TG_ARGV[0] AND entity_id = OLD.id;
    RETURN NULL;
END $$;
CREATE TRIGGER crm_deals_waits AFTER DELETE ON crm_deals FOR EACH ROW EXECUTE FUNCTION crm_waits_gone('deal');
CREATE TRIGGER crm_leads_waits AFTER DELETE ON crm_leads FOR EACH ROW EXECUTE FUNCTION crm_waits_gone('lead');
CREATE TRIGGER crm_contacts_waits AFTER DELETE ON crm_contacts FOR EACH ROW EXECUTE FUNCTION crm_waits_gone('contact');
CREATE TRIGGER crm_companies_waits AFTER DELETE ON crm_companies FOR EACH ROW EXECUTE FUNCTION crm_waits_gone('company');

ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft', 'repeat', 'bound', 'unbound', 'doc_viewed', 'doc_signed', 'link_clicked', 'copilot_fill', 'sms',
        'wait', 'wait_done', 'wait_over'));
