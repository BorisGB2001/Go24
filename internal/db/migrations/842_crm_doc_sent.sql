-- «Отправить клиенту» of a card's document into the open line's chat: the card's line «Документ
-- отправлен в чат» (its body — the document's name and the dialogue). 'chat' — the card's «Чат» (840 of the crm stream),
-- kept here too so that the order the two are deployed in does not matter.
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft', 'repeat', 'bound', 'unbound', 'doc_viewed', 'doc_signed', 'link_clicked', 'copilot_fill', 'sms',
        'wait', 'wait_done', 'wait_over', 'chat', 'doc_sent'));
