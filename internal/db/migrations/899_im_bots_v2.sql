-- Chat bots 2.0, imbot.v2.* (GO24DEV-954): how a bot gets its events — '' by the handlers of
-- imbot.register, fetch — its application takes them by imbot.v2.Event.get (im_bot_events, kept 24 hours), webhook —
-- posted to webhook_url; botToken (its sha256), hidden (in no people picker), reactions, open lines and the background
-- of its chats as the application gives them.
ALTER TABLE im_bots
    ADD COLUMN event_mode  text NOT NULL DEFAULT '' CHECK (event_mode IN ('', 'fetch', 'webhook')),
    ADD COLUMN webhook_url text NOT NULL DEFAULT '',
    ADD COLUMN bot_token   text NOT NULL DEFAULT '',
    ADD COLUMN hidden      boolean NOT NULL DEFAULT false,
    ADD COLUMN reactions   boolean NOT NULL DEFAULT true,
    ADD COLUMN openline    boolean NOT NULL DEFAULT false,
    ADD COLUMN background  text NOT NULL DEFAULT '';

CREATE TABLE im_bot_events (
    id         bigserial PRIMARY KEY, -- eventId; imbot.v2.Event.get with offset confirms those below it
    bot_id     uuid NOT NULL REFERENCES im_bots(id) ON DELETE CASCADE,
    type       text NOT NULL,
    data       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX im_bot_events_bot ON im_bot_events (bot_id, id);
