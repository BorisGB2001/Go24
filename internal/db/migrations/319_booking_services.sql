-- «Онлайн-запись»: the services of the catalog a resource provides, each with its own duration, and the
-- services of a booking — kept with their name and price as booked, so a later price of the catalog does not change it.
CREATE TABLE booking_resource_services (
    resource_id uuid NOT NULL REFERENCES booking_resources(id) ON DELETE CASCADE,
    product_id  uuid NOT NULL REFERENCES crm_products(id) ON DELETE CASCADE,
    minutes     int NOT NULL CHECK (minutes BETWEEN 5 AND 1440),
    sort        int NOT NULL DEFAULT 0,
    PRIMARY KEY (resource_id, product_id)
);

CREATE TABLE booking_services (
    id         bigserial PRIMARY KEY,
    booking_id uuid NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    product_id uuid REFERENCES crm_products(id) ON DELETE SET NULL,
    name       text NOT NULL,
    price      numeric(18, 2) NOT NULL DEFAULT 0,
    currency   text NOT NULL DEFAULT 'RUB',
    minutes    int NOT NULL DEFAULT 0,
    sort       int NOT NULL DEFAULT 0
);
CREATE INDEX booking_services_booking_idx ON booking_services (booking_id, sort);
