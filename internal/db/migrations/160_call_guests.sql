-- A guest link of a call, as the guest entry (GO24DEV-453): people without an account of go24 join the
-- call by the link while it lasts, with its password if it has one; with the lobby on, the people of the call let
-- each guest in («Впустить / Отклонить»). call_query is the call as /call?… of the people of go24 (task=, event=,
-- room=), lk_room — its LiveKit room.
CREATE TABLE call_guest_links (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token         text NOT NULL UNIQUE,
    lk_room       text NOT NULL,
    title         text NOT NULL,
    call_query    text NOT NULL,
    created_by    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    password_hash text NOT NULL DEFAULT '',
    lobby         boolean NOT NULL DEFAULT true,
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX call_guest_links_room ON call_guest_links (lk_room);

-- A guest who came by a link: the name they gave, the secret their browser keeps, whether they were let in. seen_at —
-- their page asked last (a guest who closed the lobby is not shown waiting any more).
CREATE TABLE call_guests (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id    uuid NOT NULL REFERENCES call_guest_links(id) ON DELETE CASCADE,
    name       text NOT NULL,
    secret     text NOT NULL,
    state      text NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'admitted', 'denied')),
    decided_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    seen_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX call_guests_link ON call_guests (link_id);
