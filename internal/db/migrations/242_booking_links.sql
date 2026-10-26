-- Links for clients to book themselves, as «Онлайн-запись»: a public page /bk/{token} without signing in that
-- offers the resources of the link (none listed — all of them), the days up to days_ahead and the free slots; the
-- booking it makes is «не подтверждена», and responsible_id (none — the resource's employee, else who made the link)
-- gets a notification.
CREATE TABLE booking_links (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token          text NOT NULL UNIQUE,
    name           text NOT NULL,
    description    text NOT NULL DEFAULT '',
    resource_ids   uuid[] NOT NULL DEFAULT '{}',
    days_ahead     int NOT NULL DEFAULT 30 CHECK (days_ahead BETWEEN 1 AND 365),
    responsible_id uuid REFERENCES users(id) ON DELETE SET NULL,
    active         boolean NOT NULL DEFAULT true,
    created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- A booking made by a client by a link: the link, the client's address (a few bookings an hour from one address)
-- and the client's own token — the page of the booking (/bk/b/{token}) the client is sent to.
ALTER TABLE bookings ADD COLUMN link_id uuid REFERENCES booking_links(id) ON DELETE SET NULL,
    ADD COLUMN client_ip text NOT NULL DEFAULT '',
    ADD COLUMN client_token text UNIQUE;
