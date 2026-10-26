-- «Онлайн-запись»: a resource may allow double bookings (overbooking); a booking may take more resources
-- than its own at once (an employee and a room); the waiting list holds clients with no time yet, an employee drags
-- them into the grid when a time is found.
ALTER TABLE booking_resources ADD COLUMN overbooking boolean NOT NULL DEFAULT false;

CREATE TABLE booking_extra_resources (
    booking_id  uuid NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    resource_id uuid NOT NULL REFERENCES booking_resources(id) ON DELETE CASCADE,
    PRIMARY KEY (booking_id, resource_id)
);
CREATE INDEX booking_extra_resources_resource_idx ON booking_extra_resources (resource_id);

-- resource_id — the resource the client wants, if any
CREATE TABLE booking_waitlist (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_id  uuid REFERENCES booking_resources(id) ON DELETE SET NULL,
    client_name  text NOT NULL DEFAULT '',
    client_phone text NOT NULL DEFAULT '',
    client_email text NOT NULL DEFAULT '',
    note         text NOT NULL DEFAULT '',
    contact_id   uuid REFERENCES crm_contacts(id) ON DELETE SET NULL,
    company_id   uuid REFERENCES crm_companies(id) ON DELETE SET NULL,
    deal_id      uuid REFERENCES crm_deals(id) ON DELETE SET NULL,
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
