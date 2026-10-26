-- «Повторный лид»: a new lead from a known client (a contact or company with the same phone or e-mail) is
-- linked to it at once (contact_id, company_id) and marked repeated.
ALTER TABLE crm_leads ADD COLUMN is_repeat boolean NOT NULL DEFAULT false;
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft', 'repeat'));
