-- «Обзвон» of the CRM (GO24DEV-932): the cards chosen in a list of leads or deals become a call list of their
-- clients' phones; the employee calls them one by one in the softphone, each call's result is taken from the call log.

CREATE TABLE telephony_calllists (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title      text NOT NULL,
    entity     text NOT NULL,                -- lead, deal: whose cards
    owner      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    closed_at  timestamptz                   -- «Завершить обзвон»
);
CREATE INDEX telephony_calllists_owner ON telephony_calllists (owner, created_at DESC);

CREATE TABLE telephony_calllist_items (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    list_id   uuid NOT NULL REFERENCES telephony_calllists (id) ON DELETE CASCADE,
    pos       integer NOT NULL,
    entity_id uuid NOT NULL,                 -- the card of the list's entity
    title     text NOT NULL,                 -- the card's title and its client whose phone it is, as when the list
    name      text NOT NULL,                 -- was made
    phone     text NOT NULL,
    status    text NOT NULL DEFAULT 'wait',  -- wait, answered, noanswer, skipped
    dialed_at timestamptz,                   -- «Позвонить»: the call log after it tells the result
    call_id   uuid REFERENCES telephony_calls (id) ON DELETE SET NULL,
    UNIQUE (list_id, pos)
);
