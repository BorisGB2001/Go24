-- «Чат с CoPilot» in a collab (GO24DEV-864), as «Чат с LegacyGPT» projects: a group chat of the collab
-- where CoPilot answers every message and knows the collab — its people and tasks.
ALTER TABLE collab_chats ADD COLUMN copilot boolean NOT NULL DEFAULT false;
