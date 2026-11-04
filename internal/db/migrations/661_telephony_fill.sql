-- CoPilot «заполнение полей по звонку»: when the AI last filled the client's CRM card from the conversation
ALTER TABLE telephony_calls ADD COLUMN filled_at timestamptz;
-- CoPilot's line in the card's timeline; link_clicked — of the CRM trigger «Переход по ссылке» (stream crm, 631)
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft', 'repeat', 'bound', 'unbound', 'doc_viewed', 'doc_signed', 'link_clicked', 'copilot_fill'));
