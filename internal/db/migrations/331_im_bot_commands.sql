-- Commands of chat bots, as imbot.command.* (GO24DEV-641): an application registers «/command» for its
-- bot; a message beginning with it (or a button of the bot's keyboard) goes to the command's handler as
-- ONIMCOMMANDADD, the application answers as the bot.
CREATE TABLE im_bot_commands (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id     uuid NOT NULL REFERENCES im_bots(id) ON DELETE CASCADE,
    command    text NOT NULL CHECK (length(command) BETWEEN 1 AND 50 AND command !~ '[[:space:]/]'),
    common     boolean NOT NULL DEFAULT false, -- in every chat, not only those the bot is in
    hidden     boolean NOT NULL DEFAULT false, -- not in the list shown on «/»
    extranet   boolean NOT NULL DEFAULT false, -- guests may use it too
    lang       jsonb NOT NULL DEFAULT '{}',    -- {"ru": {"title": …, "params": …}, …}: what the list shows
    handler    text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX im_bot_commands_name ON im_bot_commands (bot_id, lower(command));
