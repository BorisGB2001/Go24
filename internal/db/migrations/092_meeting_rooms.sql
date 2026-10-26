-- Meeting rooms, as «Переговорные» of the portal calendar (GO24DEV-236): the administrator keeps the list, a meeting
-- books a room, and a room is never booked twice at the same time.
CREATE TABLE meeting_rooms (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    capacity   int CHECK (capacity > 0),
    location   text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE events ADD COLUMN room_id uuid REFERENCES meeting_rooms(id) ON DELETE SET NULL;
CREATE INDEX events_room_idx ON events (room_id) WHERE room_id IS NOT NULL;
