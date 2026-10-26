-- «Списывать товары при успешном закрытии сделки» (the settings of «Складской учёт»): a won deal's timeline tells
-- the «Реализация» it made by itself — conducted ("ship_auto") or left a draft ("ship_draft").
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft'));
