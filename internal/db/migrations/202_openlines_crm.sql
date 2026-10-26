-- «Открытые линии» and CRM: the line makes a lead of a new dialogue by itself; a visitor whose phone or
-- e-mail a contact already has is bound to that contact, not made a second time.
ALTER TABLE ol_widgets
    ADD COLUMN crm_auto boolean NOT NULL DEFAULT true; -- «Создавать лид автоматически»

ALTER TABLE ol_sessions
    ADD COLUMN contact_id uuid REFERENCES crm_contacts(id) ON DELETE SET NULL,
    ADD COLUMN lead_auto  boolean NOT NULL DEFAULT false; -- the lead the line made: its responsible follows the operator

CREATE INDEX ol_sessions_lead ON ol_sessions (lead_id) WHERE lead_id IS NOT NULL;
CREATE INDEX ol_sessions_contact ON ol_sessions (contact_id) WHERE contact_id IS NOT NULL;

-- «Диалог открытой линии» in the card's timeline (its body — the dialogue's id)
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline'));
