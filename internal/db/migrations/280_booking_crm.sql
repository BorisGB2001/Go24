-- «Онлайн-запись» and CRM: a booking is of a client's contact, company and deal (each one or none); the
-- cards' timelines show it, and a client who books by a link becomes a contact with the source «Онлайн-запись».
ALTER TABLE bookings ADD COLUMN contact_id uuid REFERENCES crm_contacts(id) ON DELETE SET NULL,
    ADD COLUMN company_id uuid REFERENCES crm_companies(id) ON DELETE SET NULL,
    ADD COLUMN deal_id uuid REFERENCES crm_deals(id) ON DELETE SET NULL;
CREATE INDEX bookings_contact_idx ON bookings (contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX bookings_company_idx ON bookings (company_id) WHERE company_id IS NOT NULL;
CREATE INDEX bookings_deal_idx ON bookings (deal_id) WHERE deal_id IS NOT NULL;

INSERT INTO crm_dict (kind, code, sort) VALUES ('source', 'BOOKING', 115) ON CONFLICT (kind, code) DO NOTHING;

-- «Онлайн-запись» in the card's timeline
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking'));
