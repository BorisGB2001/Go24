-- «Онлайн-запись»: resources of the company (an employee, a room, equipment or a type of its own) with
-- their working schedule, and bookings of clients on them. A resource type with a code is one of the standard ones
-- (its name comes from the locale); the others are named by whoever made them.
CREATE TABLE booking_resource_types (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text UNIQUE,
    name       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO booking_resource_types (code) VALUES ('employee'), ('room'), ('equipment');

-- A resource works on work_days (ISO: 1 — Monday … 7 — Sunday) from work_from to work_to (minutes of the day) and is
-- booked by slots of slot_minutes; user_id — the employee the resource is (for the type «Сотрудник»).
CREATE TABLE booking_resources (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type_id      uuid NOT NULL REFERENCES booking_resource_types(id),
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    user_id      uuid REFERENCES users(id) ON DELETE SET NULL,
    slot_minutes int NOT NULL DEFAULT 30 CHECK (slot_minutes BETWEEN 5 AND 480),
    work_days    int[] NOT NULL DEFAULT '{1,2,3,4,5}',
    work_from    int NOT NULL DEFAULT 540 CHECK (work_from BETWEEN 0 AND 1440),
    work_to      int NOT NULL DEFAULT 1080 CHECK (work_to BETWEEN 0 AND 1440),
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (work_from < work_to)
);

-- A booking of a client: «не подтверждена» until the client or an employee confirms it; a cancelled one frees its time.
CREATE TABLE bookings (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_id  uuid NOT NULL REFERENCES booking_resources(id) ON DELETE CASCADE,
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz NOT NULL,
    client_name  text NOT NULL DEFAULT '',
    client_phone text NOT NULL DEFAULT '',
    client_email text NOT NULL DEFAULT '',
    note         text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'unconfirmed' CHECK (status IN ('unconfirmed', 'confirmed', 'came', 'no_show', 'cancelled')),
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (starts_at < ends_at)
);
CREATE INDEX bookings_resource_time_idx ON bookings (resource_id, starts_at);
