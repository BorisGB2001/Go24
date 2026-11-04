-- Chat bots 2.0 (GO24DEV-954): the reactions people put on the messages of a bot of imbot.v2. A reaction taken back is
-- a redaction with nothing of it left; ONIMBOTV2REACTIONCHANGE «delete» names it, and the message, by this row.
CREATE TABLE im_bot_reactions (
    event_id   text PRIMARY KEY, -- the m.reaction
    bot_id     uuid NOT NULL REFERENCES im_bots(id) ON DELETE CASCADE,
    room_id    text NOT NULL,
    target     text NOT NULL,    -- the bot's message
    reaction   text NOT NULL,    -- the emoji
    created_at timestamptz NOT NULL DEFAULT now()
);
