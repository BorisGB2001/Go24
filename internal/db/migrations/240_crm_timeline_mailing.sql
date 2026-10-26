-- A campaign letter in the timeline of its addressee's CRM card «E-mail рассылка»: body — the id of the
-- mkt_recipients row, whose state (opened, a link followed, unsubscribed) the timeline shows.
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing'));
