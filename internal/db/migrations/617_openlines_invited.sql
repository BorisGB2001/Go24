-- Open lines (GO24DEV-808): colleagues invited into a dialogue, as «Пригласить в диалог» — they see it and
-- answer the client with its operator, who stays the same.
ALTER TABLE ol_sessions ADD COLUMN invited uuid[] NOT NULL DEFAULT '{}';
CREATE INDEX ol_sessions_invited ON ol_sessions USING gin (invited);
