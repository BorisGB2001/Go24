-- im.v2.Event.subscribe, unsubscribe, get (GO24DEV-966): an employee subscribed, go24 worker reads their
-- chats as them (since — the position of that sync) and keeps what happens there as events of im.v2 — ONIMV2MESSAGEADD,
-- ONIMV2MESSAGEUPDATE, ONIMV2MESSAGEDELETE, ONIMV2REACTIONCHANGE, ONIMV2JOINCHAT — for 24 hours, until
-- im.v2.Event.get takes them by offset.
CREATE TABLE im_v2_subscriptions (
    user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    since      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE im_v2_events (
    id         bigserial PRIMARY KEY, -- eventId; im.v2.Event.get with offset confirms those below it
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type       text NOT NULL,
    data       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX im_v2_events_user ON im_v2_events (user_id, id);

-- the reactions met, so that taking one back is ONIMV2REACTIONCHANGE «delete» with its code and message
CREATE TABLE im_v2_reactions (
    event_id text NOT NULL,
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target   text NOT NULL,
    reaction text NOT NULL,
    PRIMARY KEY (event_id, user_id)
);
