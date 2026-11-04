-- «Чат» of a portal CRM card (the history kinds also keep doc_sent of 842_crm_doc_sent, GO24DEV-924): a group chat where the employees discuss a deal, lead, contact or company (its
-- responsible, observers and those who open it). One chat per card; the card's history says when it was made.
CREATE TABLE crm_chats (
    entity     text NOT NULL CHECK (entity IN ('company', 'contact', 'deal', 'lead')),
    entity_id  uuid NOT NULL,
    room_id    text NOT NULL,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entity, entity_id)
);

CREATE FUNCTION crm_chats_gone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM crm_chats WHERE entity = TG_ARGV[0] AND entity_id = OLD.id;
    RETURN NULL;
END $$;
CREATE TRIGGER crm_deals_chats AFTER DELETE ON crm_deals FOR EACH ROW EXECUTE FUNCTION crm_chats_gone('deal');
CREATE TRIGGER crm_leads_chats AFTER DELETE ON crm_leads FOR EACH ROW EXECUTE FUNCTION crm_chats_gone('lead');
CREATE TRIGGER crm_contacts_chats AFTER DELETE ON crm_contacts FOR EACH ROW EXECUTE FUNCTION crm_chats_gone('contact');
CREATE TRIGGER crm_companies_chats AFTER DELETE ON crm_companies FOR EACH ROW EXECUTE FUNCTION crm_chats_gone('company');

ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft', 'repeat', 'bound', 'unbound', 'doc_viewed', 'doc_signed', 'link_clicked', 'copilot_fill', 'sms',
        'wait', 'wait_done', 'wait_over', 'chat', 'doc_sent'));
