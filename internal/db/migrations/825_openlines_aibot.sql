-- «Открытые линии»: the AI bot on the knowledge base (GO24DEV-918) — a new dialogue of the line goes to
-- the bot first: the AI answers the client from the pages of the knowledge base space ai_space, and hands the dialogue to
-- the operators when the pages have no answer or the client asks for a person.
ALTER TABLE ol_widgets ADD COLUMN ai_bot boolean NOT NULL DEFAULT false,
    ADD COLUMN ai_space uuid REFERENCES kb_spaces_all(id) ON DELETE SET NULL;

-- the dialogue is with the AI bot (ai_bot); ai_wait — since when the client's message waits for its answer; ai_turns — how
-- many times it has answered
ALTER TABLE ol_sessions ADD COLUMN ai_bot boolean NOT NULL DEFAULT false,
    ADD COLUMN ai_wait timestamptz,
    ADD COLUMN ai_turns int NOT NULL DEFAULT 0;
CREATE INDEX ol_sessions_ai_wait_idx ON ol_sessions (ai_wait) WHERE ai_wait IS NOT NULL;
