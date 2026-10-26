-- The history of a CRM card, as in the portal timeline: an edit leaves its changed fields (JSON: field, old, new).
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged',
        'recurred', 'recurred_from', 'changed'));
