-- The the portal CRM robot «Сообщение в открытую линию»: the text goes into the client's latest dialogue of the open lines.
ALTER TABLE crm_robots DROP CONSTRAINT crm_robots_kind_check;
ALTER TABLE crm_robots ADD CONSTRAINT crm_robots_kind_check
    CHECK (kind IN ('task', 'notify', 'comment', 'activity', 'mail', 'responsible', 'stage', 'copy', 'move', 'booking',
        'fields', 'webhook', 'infocall', 'observers', 'document', 'bp', 'close', 'sign', 'sms', 'request', 'create', 'convert',
        'chat'));
