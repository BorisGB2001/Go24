-- «Поделиться слотами» of the calendar (GO24DEV-430): a link anyone opens without signing in to pick
-- a free time of one's calendar — the personal one (no members) or a joint one with colleagues, free all at once —
-- and so make a meeting with them. Rules: [{"days": [1..7], "from": minutes, "to": minutes}].
CREATE TABLE calendar_slot_links (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token       text NOT NULL UNIQUE,
    owner_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    members     uuid[] NOT NULL DEFAULT '{}',
    length      int NOT NULL DEFAULT 60,
    rules       jsonb NOT NULL DEFAULT '[]',
    days_ahead  int NOT NULL DEFAULT 30,
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX calendar_slot_links_personal_idx ON calendar_slot_links (owner_id) WHERE members = '{}';

-- the meetings guests made by the links; the meeting itself is an event of the owner (gone — cancelled)
CREATE TABLE calendar_slot_meetings (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id       uuid REFERENCES calendar_slot_links(id) ON DELETE SET NULL,
    owner_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_id      uuid REFERENCES events(id) ON DELETE SET NULL,
    token         text NOT NULL UNIQUE,
    guest_name    text NOT NULL,
    guest_contact text NOT NULL,
    note          text NOT NULL DEFAULT '',
    starts_at     timestamptz NOT NULL,
    ends_at       timestamptz NOT NULL,
    client_ip     text NOT NULL DEFAULT '',
    cancelled     boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX calendar_slot_meetings_ip_idx ON calendar_slot_meetings (client_ip, created_at);
CREATE INDEX calendar_slot_meetings_event_idx ON calendar_slot_meetings (event_id);
