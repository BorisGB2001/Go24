-- Calls in CRM (GO24DEV-504): a call with an outside number goes to the client's card — the contact,
-- the company or the lead with that phone (a new lead for an unknown number calling, when the administrators let:
-- company_settings 'telephony_lead') — as «Звонок» in its timeline (body: the call as JSON); the log keeps whose
-- card it went to.
ALTER TABLE telephony_calls
    ADD COLUMN crm_entity text NOT NULL DEFAULT '', -- lead, contact, company, deal; "" — none
    ADD COLUMN crm_id     uuid;

ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call'));
