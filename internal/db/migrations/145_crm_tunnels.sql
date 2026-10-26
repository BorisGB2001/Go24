-- «Туннели продаж» of the portal CRM: a robot copies the deal into a stage of another pipeline, or moves it there.
ALTER TABLE crm_robots DROP CONSTRAINT crm_robots_kind_check;
ALTER TABLE crm_robots ADD CONSTRAINT crm_robots_kind_check
    CHECK (kind IN ('task', 'notify', 'comment', 'activity', 'mail', 'responsible', 'stage', 'copy', 'move'));

ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged',
        'recurred', 'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline'));
