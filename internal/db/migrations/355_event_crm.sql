-- «CRM» of a portal meeting (GO24DEV-617): the deals, leads, contacts and companies the meeting is about. A card
-- deleted to the CRM's recycle bin keeps its id, so its links come back with it; until then the card is not shown.
CREATE TABLE event_crm (
    event_id   uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    entity     text NOT NULL CHECK (entity IN ('deal', 'lead', 'contact', 'company')),
    entity_id  uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, entity, entity_id)
);
CREATE INDEX event_crm_card_idx ON event_crm (entity, entity_id);
