-- CoPilot in the messenger (GO24DEV-817): a chat bot built into go24, not an application's — people
-- write to it in a private chat or mention it in a group chat, go24 worker answers through the AI connection of the
-- one who wrote. builtin names such a bot ('copilot'); it has no application and is never closed.
ALTER TABLE im_bots ADD COLUMN builtin text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX im_bots_builtin ON im_bots (builtin) WHERE builtin <> '';
